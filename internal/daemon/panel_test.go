// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakePanelist is one panel seat behind a fake llmproxy: phase 1 and phase 2
// replies come from functions of the request, every request is recorded with
// the trace_id its context carried, and it reports model as the resolved
// model id. Calls arrive concurrently.
type fakePanelist struct {
	model  string
	phase1 func(req Request) (json.RawMessage, error)
	phase2 func(req Request) (json.RawMessage, error)

	mu     sync.Mutex
	reqs   []Request
	traces []string
}

func (f *fakePanelist) CompleteWithModel(ctx context.Context, req Request) (json.RawMessage, string, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.traces = append(f.traces, traceID(ctx))
	f.mu.Unlock()
	reply := f.phase2
	if req.Schema == panelOptionsSchema {
		reply = f.phase1
	}
	raw, err := reply(req)
	return raw, f.model, err
}

func proposals(titles ...string) func(Request) (json.RawMessage, error) {
	return func(Request) (json.RawMessage, error) {
		var p panelProposals
		for _, t := range titles {
			p.Options = append(p.Options, panelProposal{Title: t, Summary: "summary of " + t})
		}
		b, _ := json.Marshal(p)
		return b, nil
	}
}

// ranking answers with the options whose titles are given, in that order,
// looked up in the request's option list, and objects to the last one.
func ranking(confidence int, wouldChange string, titles ...string) func(Request) (json.RawMessage, error) {
	return func(req Request) (json.RawMessage, error) {
		a := panelRanking{Confidence: confidence, WouldChangeMind: wouldChange, Objections: []panelObjection{}}
		for _, t := range titles {
			id := optionIDIn(req.Turn, t)
			if id == "" {
				return nil, fmt.Errorf("option %q not offered", t)
			}
			a.Ranked = append(a.Ranked, id)
		}
		a.Objections = append(a.Objections, panelObjection{OptionID: a.Ranked[len(a.Ranked)-1], Text: "too weak: " + titles[len(titles)-1]})
		b, _ := json.Marshal(a)
		return b, nil
	}
}

// optionIDIn finds an option's id in a rendered phase-2 turn ("- O3: title").
func optionIDIn(turn, title string) string {
	for _, line := range strings.Split(turn, "\n") {
		id, rest, ok := strings.Cut(strings.TrimPrefix(line, "- "), ": ")
		if ok && strings.HasPrefix(id, "O") && rest == title {
			return id
		}
	}
	return ""
}

const principlesText = "Tokens, not mTLS. Every byte framed. Fail closed."

func newPanel(t *testing.T, seats ...*fakePanelist) (*Panel, *fakeRecords) {
	t.Helper()
	rec := newFakeRecords()
	rec.seed(acme, scopeTenant, acme, "principles/engineering.md", principlesText)
	rec.seed(acme, scopeTenant, acme, "personal/health.md", "PRIVATE: not for any vendor")
	rec.seed(acme, scopeUser, "alice", "me.md", "ALICE PRIVATE")
	p := &Panel{
		Contexts:        NewContextStore(rec),
		PrinciplesPaths: []string{"principles/"},
		now:             func() time.Time { return time.UnixMilli(1793491200000) },
	}
	for i, s := range seats {
		p.Seats = append(p.Seats, PanelSeat{Alias: fmt.Sprintf("vendor%d-latest", i+1), Panelist: s})
	}
	return p, rec
}

var grpcQuestion = PanelQuestion{
	Question:   "How should the stream between services carry rotation?",
	Situation:  "Addendum: tokens, not mTLS; every byte framed. API.md stream rules.",
	Kind:       "security-design",
	Reversible: true,
}

func TestPanelMergesOptionsWithTraceability(t *testing.T) {
	a := &fakePanelist{model: "a-1", phase1: proposals("Build it on gRPC", "Custom TCP framing"), phase2: ranking(80, "", "Build it on gRPC", "Do nothing / defer")}
	b := &fakePanelist{model: "b-1", phase1: proposals("build it on gRPC.", "Reuse what the platform already has"), phase2: ranking(70, "", "Build it on gRPC", "Do nothing / defer")}
	p, _ := newPanel(t, a, b)
	run, err := p.Run(acmeCtx(), acme, grpcQuestion)
	if err != nil {
		t.Fatal(err)
	}
	opts := run.GetOptions()
	if len(opts) != 4 {
		t.Fatalf("options = %v", opts)
	}
	// The fixed options are always first, whoever proposed them.
	if !opts[0].GetFixed() || opts[0].GetTitle() != optionReuse || !opts[1].GetFixed() || opts[1].GetTitle() != optionDefer {
		t.Fatalf("fixed options = %v %v", opts[0], opts[1])
	}
	if got := proposers(opts[0]); got != "vendor2-latest/b-1" {
		t.Fatalf("reuse proposed by %q", got)
	}
	if got := proposers(opts[1]); got != "" {
		t.Fatalf("defer proposed by %q, want nobody", got)
	}
	// "Build it on gRPC" and "build it on gRPC." are one option, proposed by both.
	if opts[2].GetTitle() != "Build it on gRPC" || proposers(opts[2]) != "vendor1-latest/a-1,vendor2-latest/b-1" {
		t.Fatalf("merged option = %v", opts[2])
	}
	if opts[3].GetTitle() != "Custom TCP framing" || proposers(opts[3]) != "vendor1-latest/a-1" {
		t.Fatalf("option = %v", opts[3])
	}
	for i, o := range opts {
		if o.GetOptionId() != fmt.Sprintf("O%d", i+1) {
			t.Fatalf("option ids = %v", opts)
		}
	}
}

func proposers(o *pb.PanelOption) string {
	var s []string
	for _, m := range o.GetProposedBy() {
		s = append(s, m.GetModelAlias()+"/"+m.GetResolvedModel())
	}
	return strings.Join(s, ",")
}

// Phase 1 sees the question and the situation only; phase 2 adds the
// principles files and nothing else from the context; every panelist answers
// under every lens.
func TestPanelSendsOnlyQuestionSituationAndPrinciples(t *testing.T) {
	a := &fakePanelist{model: "a-1", phase1: proposals("Build it on gRPC"), phase2: ranking(80, "", "Build it on gRPC")}
	p, _ := newPanel(t, a)
	if _, err := p.Run(acmeCtx(), acme, grpcQuestion); err != nil {
		t.Fatal(err)
	}
	if len(a.reqs) != 1+len(panelLenses) {
		t.Fatalf("calls = %d, want one phase-1 and one per lens", len(a.reqs))
	}
	lenses := map[string]bool{}
	for i, req := range a.reqs {
		all := req.System + req.Repo + req.Turn
		if strings.Contains(all, "PRIVATE") {
			t.Fatalf("request %d carries context beyond the principles: %q", i, all)
		}
		if !strings.Contains(all, grpcQuestion.Question) || !strings.Contains(all, grpcQuestion.Situation) {
			t.Fatalf("request %d lacks the question or situation", i)
		}
		if req.Schema == panelOptionsSchema {
			if strings.Contains(all, principlesText) || strings.Contains(all, "Jody") {
				t.Fatalf("phase 1 is anchored on the person: %q", all)
			}
			continue
		}
		if !strings.Contains(all, principlesText) {
			t.Fatalf("phase 2 request %d lacks the principles", i)
		}
		for _, l := range panelLenses {
			if strings.Contains(req.Turn, l.instruction) {
				lenses[l.lens.String()] = true
			}
		}
	}
	if len(lenses) != len(panelLenses) {
		t.Fatalf("lenses used = %v", lenses)
	}
}

func TestPanelFailsClosedWithoutPrinciples(t *testing.T) {
	a := &fakePanelist{model: "a-1", phase1: proposals("x"), phase2: ranking(1, "", "x")}
	for _, paths := range [][]string{nil, {"nothing/here/"}, {"principles"}} {
		p, _ := newPanel(t, a)
		p.PrinciplesPaths = paths
		_, err := p.Run(acmeCtx(), acme, grpcQuestion)
		if paths != nil && paths[0] == "principles" {
			// A directory named without its slash is still that directory.
			if err != nil {
				t.Fatalf("paths %v: %v", paths, err)
			}
			continue
		}
		if !errors.Is(err, errNoPrinciples) {
			t.Fatalf("paths %v: err = %v, want errNoPrinciples", paths, err)
		}
	}
	// "principles" must not match "principles-old/x.md".
	p, rec := newPanel(t, a)
	rec.seed(acme, scopeTenant, acme, "principles-old/x.md", "OLD PRIVATE")
	before := len(a.reqs)
	if _, err := p.Run(acmeCtx(), acme, grpcQuestion); err != nil {
		t.Fatal(err)
	}
	for _, req := range a.reqs[before:] {
		if strings.Contains(req.Turn, "OLD PRIVATE") {
			t.Fatal("a sibling path matched a principles prefix")
		}
	}
	if len(a.reqs) == 0 {
		t.Fatal("no calls")
	}
}

func TestPanelFailsClosedBeforeAnyVendorCall(t *testing.T) {
	a := &fakePanelist{model: "a-1", phase1: proposals("x"), phase2: ranking(1, "", "x")}
	p, _ := newPanel(t, a)
	p.PrinciplesPaths = []string{"missing.md"}
	if _, err := p.Run(acmeCtx(), acme, grpcQuestion); !errors.Is(err, errNoPrinciples) {
		t.Fatalf("err = %v", err)
	}
	if len(a.reqs) != 0 {
		t.Fatalf("%d vendor calls made before failing closed", len(a.reqs))
	}
	p.Seats = nil
	p.PrinciplesPaths = []string{"principles/"}
	if _, err := p.Run(acmeCtx(), acme, grpcQuestion); !errors.Is(err, errNoPanel) {
		t.Fatalf("no seats err = %v", err)
	}
}

// The tally is plain code: Borda points, first places, agreement, and every
// dissent and every objection to the top option word for word.
func TestPanelTallyAndDissent(t *testing.T) {
	a := &fakePanelist{model: "a-1", phase1: proposals("Build it on gRPC", "Custom TCP framing"), phase2: ranking(90, "a binary stream already exists", "Build it on gRPC", "Custom TCP framing")}
	b := &fakePanelist{model: "b-1", phase1: proposals("Build it on gRPC"), phase2: ranking(60, "gRPC is banned", "Custom TCP framing", "Build it on gRPC")}
	p, _ := newPanel(t, a, b)
	run, err := p.Run(acmeCtx(), acme, grpcQuestion)
	if err != nil {
		t.Fatal(err)
	}
	res := run.GetResult()
	if res.GetProvenance() != pb.Provenance_PROVENANCE_DERIVED_PANEL {
		t.Fatalf("provenance = %v", res.GetProvenance())
	}
	// Five answers each from a and b. Each ranks two of four options:
	// first gets 4 points, second 3. gRPC: 5*4 + 5*3 = 35; TCP: 5*3 + 5*4 = 35.
	// A tie in points and first places goes to the earlier option.
	grpcID, tcpID := "O3", "O4"
	tally := map[string]*pb.PanelOptionTally{}
	for _, t := range res.GetTallies() {
		tally[t.GetOptionId()] = t
	}
	if tally[grpcID].GetPoints() != 35 || tally[tcpID].GetPoints() != 35 || tally[grpcID].GetFirstPlace() != 5 || tally["O1"].GetPoints() != 0 {
		t.Fatalf("tallies = %v", res.GetTallies())
	}
	if res.GetTopOptionId() != grpcID || res.GetAgreement() != 50 {
		t.Fatalf("top = %s agreement = %d", res.GetTopOptionId(), res.GetAgreement())
	}
	if len(res.GetDissents()) != len(panelLenses) {
		t.Fatalf("dissents = %d, want b's %d", len(res.GetDissents()), len(panelLenses))
	}
	for _, d := range res.GetDissents() {
		if d.GetModel().GetResolvedModel() != "b-1" || d.GetRankedFirst() != tcpID || d.GetWouldChangeMind() != "gRPC is banned" ||
			len(d.GetObjections()) != 1 || d.GetObjections()[0].GetText() != "too weak: Build it on gRPC" {
			t.Fatalf("dissent = %v", d)
		}
	}
	if len(res.GetObjectionsToTop()) != len(panelLenses) || res.GetObjectionsToTop()[0].GetText() != "too weak: Build it on gRPC" {
		t.Fatalf("objections to top = %v", res.GetObjectionsToTop())
	}
	if len(res.GetWouldChangeMind()) != 2*len(panelLenses) {
		t.Fatalf("would change mind = %v", res.GetWouldChangeMind())
	}
	if res.GetDisposition() != pb.PanelDisposition_PANEL_DISPOSITION_TO_PERSON {
		t.Fatal("dissent must go to the person")
	}
	// Every answer carries its model, lens and time.
	for _, ans := range run.GetAnswers() {
		if ans.GetModel().GetModelAlias() == "" || ans.GetLens() == pb.PanelLens_PANEL_LENS_UNSPECIFIED || ans.GetAnsweredAtMs() != 1793491200000 {
			t.Fatalf("answer = %v", ans)
		}
	}
	// The same answers tally the same way.
	again := tallyPanel(run.GetOptions(), run.GetAnswers(), grpcQuestion, len(run.GetFailures()))
	if again.String() != res.String() {
		t.Fatal("the tally is not reproducible")
	}
}

func TestPanelDisposition(t *testing.T) {
	agree := func() []*fakePanelist {
		return []*fakePanelist{
			{model: "a-1", phase1: proposals("Build it on gRPC"), phase2: ranking(90, "", "Build it on gRPC")},
			{model: "b-1", phase1: proposals("Build it on gRPC"), phase2: ranking(90, "", "Build it on gRPC")},
		}
	}
	for _, c := range []struct {
		name string
		q    PanelQuestion
		want pb.PanelDisposition
	}{
		{"unanimous reversible", PanelQuestion{Question: "q", Situation: "s", Reversible: true}, pb.PanelDisposition_PANEL_DISPOSITION_MAY_ACT},
		{"irreversible", PanelQuestion{Question: "q", Situation: "s"}, pb.PanelDisposition_PANEL_DISPOSITION_TO_PERSON},
		{"security", PanelQuestion{Question: "q", Situation: "s", Reversible: true, TouchesSecurityOrIdentity: true}, pb.PanelDisposition_PANEL_DISPOSITION_TO_PERSON},
	} {
		p, _ := newPanel(t, agree()...)
		run, err := p.Run(acmeCtx(), acme, c.q)
		if err != nil {
			t.Fatal(err)
		}
		if got := run.GetResult().GetDisposition(); got != c.want {
			t.Errorf("%s: disposition = %v, want %v", c.name, got, c.want)
		}
		if run.GetResult().GetAgreement() != 100 {
			t.Errorf("%s: agreement = %d", c.name, run.GetResult().GetAgreement())
		}
	}
	// A panelist that never answered is reported, and a panel missing a voice
	// is not agreement.
	seats := agree()
	seats = append(seats, &fakePanelist{model: "c-1", phase1: func(Request) (json.RawMessage, error) { return nil, errors.New("vendor down") }})
	p, _ := newPanel(t, seats...)
	run, err := p.Run(acmeCtx(), acme, PanelQuestion{Question: "q", Situation: "s", Reversible: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(run.GetFailures()) != 1 || run.GetFailures()[0].GetPhase() != 1 || !strings.Contains(run.GetFailures()[0].GetError(), "vendor down") {
		t.Fatalf("failures = %v", run.GetFailures())
	}
	if run.GetResult().GetDisposition() != pb.PanelDisposition_PANEL_DISPOSITION_TO_PERSON {
		t.Fatal("a missing panelist must send it to the person")
	}
}

// An answer outside the schema (an unknown option, a repeat, a confidence out
// of range) is rejected with the reason and asked again; one that never
// conforms is a reported failure, not a vote.
func TestPanelRejectsInvalidRankings(t *testing.T) {
	var calls int
	var mu sync.Mutex
	flaky := func(req Request) (json.RawMessage, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if !strings.Contains(req.Turn, "previous answer was rejected") {
			return json.RawMessage(fmt.Sprintf(`{"ranked_option_ids":["O9","O%d"],"confidence":50,"objections":[],"would_change_mind":""}`, n)), nil
		}
		return ranking(50, "", "Build it on gRPC")(req)
	}
	a := &fakePanelist{model: "a-1", phase1: proposals("Build it on gRPC"), phase2: flaky}
	b := &fakePanelist{model: "b-1", phase1: proposals("Build it on gRPC"), phase2: func(Request) (json.RawMessage, error) {
		return json.RawMessage(`{"ranked_option_ids":["O1","O1"],"confidence":500,"objections":[],"would_change_mind":""}`), nil
	}}
	p, _ := newPanel(t, a, b)
	run, err := p.Run(acmeCtx(), acme, grpcQuestion)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.GetAnswers()) != len(panelLenses) {
		t.Fatalf("answers = %d, want a's %d", len(run.GetAnswers()), len(panelLenses))
	}
	if len(run.GetFailures()) != len(panelLenses) {
		t.Fatalf("failures = %v", run.GetFailures())
	}
	for _, f := range run.GetFailures() {
		if f.GetPhase() != 2 || f.GetModel().GetResolvedModel() != "b-1" {
			t.Fatalf("failure = %v", f)
		}
	}
	if got := len(b.reqs); got != 1+maxAttempts*len(panelLenses) {
		t.Fatalf("b calls = %d", got)
	}
}

func TestValidRanking(t *testing.T) {
	known := map[string]bool{"O1": true, "O2": true}
	for _, c := range []struct {
		r    panelRanking
		want string
	}{
		{panelRanking{Ranked: []string{"O2", "O1"}, Confidence: 0}, ""},
		{panelRanking{Ranked: nil}, "ranks no option"},
		{panelRanking{Ranked: []string{"O3"}}, "unknown option"},
		{panelRanking{Ranked: []string{"O1", "O1"}}, "twice"},
		{panelRanking{Ranked: []string{"O1"}, Confidence: 101}, "confidence"},
		{panelRanking{Ranked: []string{"O1"}, Confidence: -1}, "confidence"},
		{panelRanking{Ranked: []string{"O1"}, Objections: []panelObjection{{OptionID: "O7", Text: "x"}}}, "objection"},
	} {
		err := validRanking(c.r, known)
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("validRanking(%+v) = %v, want %q", c.r, err, c.want)
		}
	}
}

func TestNormalizeOptionTitle(t *testing.T) {
	for _, c := range [][2]string{
		{"Build it on gRPC", "build it on grpc"},
		{"  build   it on gRPC. ", "build it on grpc"},
		{"Do nothing / defer", "do nothing defer"},
		{"Reuse what the platform already has!", "reuse what the platform already has"},
	} {
		if got := normalizeOptionTitle(c[0]); got != c[1] {
			t.Errorf("normalize(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

// The handler keeps every run as a PanelRun, records the person's decision
// (an option of that run), and scores the decided runs per model and question
// kind.
func TestServerPanelRunDecisionAndTrackRecord(t *testing.T) {
	a := &fakePanelist{model: "a-1", phase1: proposals("Build it on gRPC", "Custom TCP framing"), phase2: ranking(90, "", "Build it on gRPC", "Custom TCP framing")}
	b := &fakePanelist{model: "b-1", phase1: proposals("Build it on gRPC"), phase2: ranking(60, "", "Custom TCP framing", "Build it on gRPC")}
	panel, rec := newPanel(t, a, b)
	s := &Server{Panel: panel, Panels: NewPanelStore(rec)}
	ctx := acmeCtx()
	alice := asUser("alice")

	if _, err := s.GeneratePanelAnswer(ctx, nil, &pb.GeneratePanelAnswerRequest{Tenant: acmeTid, Question: "q", Situation: "s"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no caller err = %v", err)
	}
	resp, err := s.GeneratePanelAnswer(ctx, alice, &pb.GeneratePanelAnswerRequest{Tenant: acmeTid,
		Question: grpcQuestion.Question, Situation: grpcQuestion.Situation, QuestionKind: "security-design", Reversible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetPanelId() == "" || resp.GetResult().GetTopOptionId() != "O3" || len(resp.GetAnswers()) != 2*len(panelLenses) {
		t.Fatalf("resp = %v", resp)
	}
	stored := rec.panelRuns[resp.GetPanelId()]
	if stored == nil || stored.GetOwner() != "alice" || stored.GetTenantNamespace() != acme || stored.GetPerson() != "Jody" || stored.GetQuestionKind() != "security-design" {
		t.Fatalf("stored run = %v", stored)
	}

	// Nothing decided yet: nothing scored.
	tr, err := s.GetPanelTrackRecord(ctx, alice, &pb.GetPanelTrackRecordRequest{Tenant: acmeTid})
	if err != nil || tr.GetDecidedRuns() != 0 || len(tr.GetRecords()) != 0 {
		t.Fatalf("track record before a decision = %v, %v", tr, err)
	}
	if _, err := s.UpdatePanelDecision(ctx, asUser("jody"), &pb.UpdatePanelDecisionRequest{Tenant: acmeTid, PanelId: resp.GetPanelId(), OptionId: "O99"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown option err = %v", err)
	}
	if _, err := s.UpdatePanelDecision(ctx, asUser("jody"), &pb.UpdatePanelDecisionRequest{Tenant: acmeTid, PanelId: "0123456789abcdef0123456789abcdef", OptionId: "O3"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown run err = %v", err)
	}
	if _, err := s.UpdatePanelDecision(ctx, asUser("jody"), &pb.UpdatePanelDecisionRequest{Tenant: acmeTid, PanelId: resp.GetPanelId(), OptionId: "O3"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.panelRuns[resp.GetPanelId()]; got.GetDecidedOptionId() != "O3" || got.GetDecidedAtMs() == 0 {
		t.Fatalf("decision not stored: %v", got)
	}
	// A second instance with no cache reads the same record from the store.
	s2 := &Server{Panel: panel, Panels: NewPanelStore(rec)}
	tr, err = s2.GetPanelTrackRecord(ctx, alice, &pb.GetPanelTrackRecordRequest{Tenant: acmeTid})
	if err != nil {
		t.Fatal(err)
	}
	if tr.GetDecidedRuns() != 1 || len(tr.GetRecords()) != 2 {
		t.Fatalf("track record = %v", tr)
	}
	byModel := map[string]*pb.PanelTrackRecord{}
	for _, r := range tr.GetRecords() {
		byModel[r.GetModel().GetResolvedModel()] = r
		if r.GetQuestionKind() != "security-design" || r.GetRuns() != 1 || r.GetAnswers() != int32(len(panelLenses)) {
			t.Fatalf("record = %v", r)
		}
	}
	if byModel["a-1"].GetTopHits() != int32(len(panelLenses)) || byModel["a-1"].GetMeanRank() != 1 {
		t.Fatalf("a-1 = %v", byModel["a-1"])
	}
	if byModel["b-1"].GetTopHits() != 0 || byModel["b-1"].GetMeanRank() != 2 {
		t.Fatalf("b-1 = %v", byModel["b-1"])
	}
}

func TestServerPanelErrors(t *testing.T) {
	rec := newFakeRecords()
	s := &Server{Panels: NewPanelStore(rec)}
	if _, err := s.GeneratePanelAnswer(acmeCtx(), asUser("alice"), &pb.GeneratePanelAnswerRequest{Tenant: acmeTid, Question: "q", Situation: "s"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("no panel err = %v, want FailedPrecondition", err)
	}
	a := &fakePanelist{model: "a-1", phase1: proposals("x"), phase2: ranking(1, "", "x")}
	panel, _ := newPanel(t, a)
	panel.PrinciplesPaths = []string{"missing/"}
	s.Panel = panel
	if _, err := s.GeneratePanelAnswer(acmeCtx(), asUser("alice"), &pb.GeneratePanelAnswerRequest{Tenant: acmeTid, Question: "q", Situation: "s"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("no principles err = %v, want FailedPrecondition", err)
	}
}

// The config delivery builds one seat per panel_model_aliases entry, through
// NewPanelist, over the same context store as the engine; no aliases is no
// panel, and aliases with no constructor wired is a refused config.
func TestServerConfigBuildsPanel(t *testing.T) {
	rec := newFakeRecords()
	var asked []string
	s := &Server{
		Records:     rec,
		NewProvider: func(string) Provider { return &scriptedProvider{} },
		NewPanelist: func(alias string) Panelist { asked = append(asked, alias); return &fakePanelist{} },
	}
	cfg := &pb.Config{ModelAlias: "m", PanelModelAliases: []string{"openai-latest", "xai-latest", "gemini-latest"}, PrinciplesPaths: []string{"principles/"}}
	if err := s.DoppelgangerConfig(cfg); err != nil {
		t.Fatal(err)
	}
	panel, runs := s.currentPanel()
	if panel == nil || runs == nil || len(panel.Seats) != 3 || panel.Seats[1].Alias != "xai-latest" || strings.Join(asked, ",") != "openai-latest,xai-latest,gemini-latest" {
		t.Fatalf("panel = %+v asked = %v", panel, asked)
	}
	if panel.Contexts != s.Engine.Contexts || panel.PrinciplesPaths[0] != "principles/" {
		t.Fatal("panel does not share the engine's context store or lost principles_paths")
	}
	if err := s.DoppelgangerConfig(&pb.Config{ModelAlias: "m"}); err != nil {
		t.Fatal(err)
	}
	if panel, _ := s.currentPanel(); panel != nil {
		t.Fatal("no aliases must mean no panel")
	}
	s.NewPanelist = nil
	if err := s.DoppelgangerConfig(cfg); err == nil {
		t.Fatal("aliases with no panelist constructor must be refused")
	}
}
