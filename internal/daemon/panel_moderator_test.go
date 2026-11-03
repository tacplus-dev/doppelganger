// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	llmproxy_gc "github.com/tacplus-dev/doppelganger/tacplus/s2sm/grpc/clients/services/llmproxy"
	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	llmproxy_pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/llmproxy"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// fakeModerator is the panel's moderator behind a fake llmproxy: each of its
// three steps answers from a function of the request (nil: the step fails),
// and every request is recorded with the trace_id its context carried.
type fakeModerator struct {
	model   string
	frame   func(req Request) (json.RawMessage, error)
	merge   func(req Request) (json.RawMessage, error)
	summary func(req Request) (json.RawMessage, error)

	mu     sync.Mutex
	reqs   []Request
	traces []string
}

func (f *fakeModerator) CompleteWithModel(ctx context.Context, req Request) (json.RawMessage, string, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.traces = append(f.traces, traceID(ctx))
	f.mu.Unlock()
	var reply func(Request) (json.RawMessage, error)
	switch req.Schema {
	case panelFrameSchema:
		reply = f.frame
	case panelMergeSchema:
		reply = f.merge
	case panelSummarySchema:
		reply = f.summary
	default:
		return nil, f.model, fmt.Errorf("the moderator was sent a panelist's request: %q", req.Schema)
	}
	if reply == nil {
		return nil, f.model, errors.New("moderator down")
	}
	raw, err := reply(req)
	return raw, f.model, err
}

func jsonReply(v any) func(Request) (json.RawMessage, error) {
	return func(Request) (json.RawMessage, error) {
		b, err := json.Marshal(v)
		return b, err
	}
}

var grpcFraming = panelFraming{
	Question:     "How should a service-to-service stream carry credential rotation?",
	Requirements: []string{"tokens, not mTLS", "every byte framed"},
	Situation:    "API.md's stream rules apply.",
}

// mergeAll is a moderator merge that puts every proposal whose title
// contains a key into one option per key, in key order, and the fixed
// options in their own.
func mergeAll(keys ...string) func(Request) (json.RawMessage, error) {
	return func(req Request) (json.RawMessage, error) {
		out := panelMerged{Options: []panelMergedOption{
			{Title: optionReuse, Summary: "reuse", Members: []string{"F1"}},
			{Title: optionDefer, Summary: "defer", Members: []string{"F2"}},
		}}
		for _, k := range keys {
			out.Options = append(out.Options, panelMergedOption{Title: k, Summary: "merged " + k})
		}
		for _, line := range strings.Split(req.Turn, "\n") {
			id, title, ok := strings.Cut(strings.TrimPrefix(line, "- "), ": ")
			if !ok || !strings.HasPrefix(id, "P") {
				continue
			}
			for i, k := range keys {
				if strings.Contains(strings.ToLower(title), strings.ToLower(k)) {
					out.Options[2+i].Members = append(out.Options[2+i].Members, id)
					break
				}
			}
		}
		b, err := json.Marshal(out)
		return b, err
	}
}

// quoteEverything is a summary that names the top option and quotes every
// dissent label and text it is given, as the summary prompt asks.
func quoteEverything(req Request) (json.RawMessage, error) {
	b, err := json.Marshal(panelSummary{Summary: "Summary.\n" + req.Turn})
	return b, err
}

// Jody's composition (2026-10-31): Claude moderates and is panelist #1, OpenAI
// #2, Gemini #3.
func newModeratedPanel(t *testing.T, mod *fakeModerator, seats ...*fakePanelist) *Panel {
	t.Helper()
	p, _ := newPanel(t, seats...)
	names := []string{"claude-opus-latest", "openai-latest", "gemini-latest"}
	for i := range p.Seats {
		p.Seats[i].Alias = names[i]
	}
	p.Moderator = &PanelSeat{Alias: "claude-opus-latest", Panelist: mod}
	return p
}

// The Claude panelist is fed the same information and nothing more, just like
// the other two (Jody, 2026-10-31): in each phase, and under each lens, every
// seat's request is byte-identical, and none carries anything of the
// moderator's own calls.
func TestPanelSeatsGetByteIdenticalInputs(t *testing.T) {
	mod := &fakeModerator{model: "claude-opus-5-5", frame: jsonReply(grpcFraming), merge: mergeAll("Build it on gRPC", "TCP"), summary: quoteEverything}
	claude := &fakePanelist{model: "claude-opus-5-5", phase1: proposals("Build it on gRPC", "Custom TCP framing"), phase2: ranking(80, "", "Build it on gRPC")}
	openai := &fakePanelist{model: "gpt-5.2", phase1: proposals("Build it on gRPC"), phase2: ranking(70, "", "Build it on gRPC")}
	gemini := &fakePanelist{model: "gemini-3-pro", phase1: proposals("Raw TCP framing"), phase2: ranking(60, "", "TCP")}
	p := newModeratedPanel(t, mod, claude, openai, gemini)
	run, err := p.Run(context.Background(), acme, grpcQuestion)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.GetFailures()) != 0 {
		t.Fatalf("failures = %v", run.GetFailures())
	}
	byPhase := func(f *fakePanelist) (phase1 []Request, phase2 []string) {
		for _, r := range f.reqs {
			if r.Schema == panelOptionsSchema {
				phase1 = append(phase1, r)
				continue
			}
			b, _ := json.Marshal(r)
			phase2 = append(phase2, string(b))
		}
		sort.Strings(phase2) // calls arrive concurrently; one per lens
		return phase1, phase2
	}
	c1, c2 := byPhase(claude)
	if len(c1) != 1 || len(c2) != len(panelLenses) {
		t.Fatalf("claude calls = %d + %d", len(c1), len(c2))
	}
	for name, other := range map[string]*fakePanelist{"openai": openai, "gemini": gemini} {
		o1, o2 := byPhase(other)
		if !reflect.DeepEqual(o1, c1) {
			t.Fatalf("phase 1: claude's request differs from %s's:\n%+v\n%+v", name, c1, o1)
		}
		if !reflect.DeepEqual(o2, c2) {
			t.Fatalf("phase 2: claude's requests differ from %s's", name)
		}
	}
	// The framing reached every seat; the moderator's prompts, and the
	// proposals as the moderator saw them, reached none.
	if !strings.Contains(c1[0].Turn, grpcFraming.Question) || !strings.Contains(c1[0].Turn, "- tokens, not mTLS") {
		t.Fatalf("phase 1 lacks the framing: %q", c1[0].Turn)
	}
	for _, r := range claude.reqs {
		for _, mr := range mod.reqs {
			if r.System == mr.System || strings.Contains(r.Turn, mr.Turn) {
				t.Fatal("a moderator call fed the claude panelist")
			}
		}
	}
	if run.GetModerator().GetModelAlias() != "claude-opus-latest" || run.GetModerator().GetResolvedModel() != "claude-opus-5-5" ||
		run.GetFramedQuestion() != grpcFraming.Question || len(run.GetFramedRequirements()) != 2 || !run.GetModeratorMerged() {
		t.Fatalf("run = moderator %v framed %q %v merged %v", run.GetModerator(), run.GetFramedQuestion(), run.GetFramedRequirements(), run.GetModeratorMerged())
	}
	// The track record keeps each panelist apart, claude included.
	seen := map[string]bool{}
	for _, a := range run.GetAnswers() {
		seen[a.GetModel().GetModelAlias()+"/"+a.GetModel().GetResolvedModel()] = true
	}
	if len(seen) != 3 || !seen["claude-opus-latest/claude-opus-5-5"] {
		t.Fatalf("answer models = %v", seen)
	}
}

// The moderator is given neither the principles, nor any other context, nor
// who proposed what; it frames from the caller's question and situation alone.
func TestModeratorSeesNoPrinciplesNorProposers(t *testing.T) {
	mod := &fakeModerator{model: "m-1", frame: jsonReply(grpcFraming), merge: mergeAll("Build it on gRPC"), summary: quoteEverything}
	a := &fakePanelist{model: "a-1", phase1: proposals("Build it on gRPC"), phase2: ranking(80, "", "Build it on gRPC")}
	p := newModeratedPanel(t, mod, a)
	if _, err := p.Run(context.Background(), acme, grpcQuestion); err != nil {
		t.Fatal(err)
	}
	if len(mod.reqs) != 3 {
		t.Fatalf("moderator calls = %d, want frame, merge and summary", len(mod.reqs))
	}
	for i, r := range mod.reqs {
		all := r.System + r.Repo + r.Turn
		for _, banned := range []string{principlesText, "PRIVATE", "claude-opus-latest", "a-1", "Jody"} {
			if strings.Contains(all, banned) {
				t.Fatalf("moderator request %d carries %q", i, banned)
			}
		}
	}
	if !strings.Contains(mod.reqs[0].Turn, grpcQuestion.Question) || !strings.Contains(mod.reqs[0].Turn, grpcQuestion.Situation) {
		t.Fatal("the framing request lacks the caller's question or situation")
	}
}

// The moderator's merge is checked by code: every proposal in exactly one
// option, no option without one, the fixed options apart. A merge that
// breaks it is asked again with the reason; every proposer is kept, word for
// word, by code, not by the moderator.
func TestModeratorMergeIsCheckedByCode(t *testing.T) {
	calls := 0
	bad := mergeAll("gRPC")
	mod := &fakeModerator{model: "m-1", frame: jsonReply(grpcFraming), summary: quoteEverything, merge: func(req Request) (json.RawMessage, error) {
		calls++
		if calls == 1 {
			return bad(req) // drops "Custom TCP framing"
		}
		_, rejection, _ := strings.Cut(req.Turn, "Your previous answer was rejected")
		if !strings.Contains(rejection, "P2") {
			return nil, fmt.Errorf("retry lacks the reason: %q", req.Turn)
		}
		return mergeAll("gRPC", "TCP")(req)
	}}
	a := &fakePanelist{model: "a-1", phase1: proposals("Build it on gRPC", "Custom TCP framing"), phase2: ranking(80, "", "gRPC")}
	b := &fakePanelist{model: "b-1", phase1: proposals("gRPC stream with rotation"), phase2: ranking(70, "", "gRPC")}
	p := newModeratedPanel(t, mod, a, b)
	run, err := p.Run(context.Background(), acme, grpcQuestion)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !run.GetModeratorMerged() {
		t.Fatalf("merge calls = %d merged = %v", calls, run.GetModeratorMerged())
	}
	opts := run.GetOptions()
	if len(opts) != 4 || opts[2].GetTitle() != "gRPC" || opts[2].GetSummary() != "merged gRPC" {
		t.Fatalf("options = %v", opts)
	}
	if proposers(opts[2]) != "claude-opus-latest/a-1,openai-latest/b-1" || len(opts[2].GetProposals()) != 2 ||
		opts[2].GetProposals()[1].GetTitle() != "gRPC stream with rotation" || opts[2].GetProposals()[1].GetModel().GetResolvedModel() != "b-1" {
		t.Fatalf("merged option lost its proposers: %v", opts[2])
	}
	if !opts[0].GetFixed() || opts[0].GetTitle() != optionReuse || !opts[1].GetFixed() || opts[1].GetTitle() != optionDefer {
		t.Fatalf("fixed options = %v %v", opts[0], opts[1])
	}
}

func TestValidMerge(t *testing.T) {
	ids := []string{"F1", "F2", "P1", "P2"}
	opt := func(title string, members ...string) panelMergedOption {
		return panelMergedOption{Title: title, Summary: "s", Members: members}
	}
	ok := panelMerged{Options: []panelMergedOption{opt("r", "F1"), opt("d", "F2"), opt("x", "P1", "P2")}}
	if err := validMerge(ok, ids); err != nil {
		t.Fatalf("valid merge refused: %v", err)
	}
	for name, m := range map[string]panelMerged{
		"a proposal left out":     {Options: []panelMergedOption{opt("r", "F1"), opt("d", "F2"), opt("x", "P1")}},
		"a proposal twice":        {Options: []panelMergedOption{opt("r", "F1"), opt("d", "F2"), opt("x", "P1", "P2"), opt("y", "P2")}},
		"an unknown id":           {Options: []panelMergedOption{opt("r", "F1"), opt("d", "F2"), opt("x", "P1", "P2", "P9")}},
		"an invented option":      {Options: []panelMergedOption{opt("r", "F1"), opt("d", "F2"), opt("x", "P1", "P2"), opt("new")}},
		"the fixed ones together": {Options: []panelMergedOption{opt("r", "F1", "F2"), opt("x", "P1", "P2")}},
		"an untitled option":      {Options: []panelMergedOption{opt("r", "F1"), opt("d", "F2"), opt(" ", "P1", "P2")}},
	} {
		var invalid *invalidPanelAnswer
		if err := validMerge(m, ids); !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v, want an invalid answer", name, err)
		}
	}
}

// A moderator that keeps failing changes nothing the panel counts: the
// caller's text is used as given, code merges, there is no summary, and each
// step is reported as a failure. The tally is the one code makes without a
// moderator.
func TestModeratorFailureFallsBackToCode(t *testing.T) {
	seats := func() []*fakePanelist {
		return []*fakePanelist{
			{model: "a-1", phase1: proposals("Build it on gRPC"), phase2: ranking(90, "", "Build it on gRPC")},
			{model: "b-1", phase1: proposals("Build it on gRPC"), phase2: ranking(90, "", "Build it on gRPC")},
		}
	}
	plain, _ := newPanel(t, seats()...)
	want, err := plain.Run(context.Background(), acme, grpcQuestion)
	if err != nil {
		t.Fatal(err)
	}
	mod := &fakeModerator{model: "m-1"}
	p := newModeratedPanel(t, mod, seats()...)
	run, err := p.Run(context.Background(), acme, grpcQuestion)
	if err != nil {
		t.Fatal(err)
	}
	steps := map[pb.PanelModeratorStep]bool{}
	for _, f := range run.GetFailures() {
		if f.GetPhase() != 0 || f.GetModel().GetModelAlias() != "claude-opus-latest" {
			t.Fatalf("failure = %v", f)
		}
		steps[f.GetModeratorStep()] = true
	}
	if len(run.GetFailures()) != 3 || len(steps) != 3 {
		t.Fatalf("failures = %v", run.GetFailures())
	}
	if run.GetFramedQuestion() != grpcQuestion.Question || run.GetFramedSituation() != grpcQuestion.Situation || run.GetModeratorMerged() || run.GetResult().GetSummary() != "" {
		t.Fatalf("fallback run = %q %q merged %v summary %q", run.GetFramedQuestion(), run.GetFramedSituation(), run.GetModeratorMerged(), run.GetResult().GetSummary())
	}
	// Same options and the same tally; a moderator failure is no dissent and
	// no missing panelist, so the disposition stands too.
	if fmt.Sprint(titles(run.GetOptions())) != fmt.Sprint(titles(want.GetOptions())) || run.GetResult().String() != want.GetResult().String() {
		t.Fatalf("moderator failure changed the result:\n%v\n%v", run.GetResult(), want.GetResult())
	}
	if run.GetResult().GetDisposition() != pb.PanelDisposition_PANEL_DISPOSITION_MAY_ACT {
		t.Fatalf("disposition = %v", run.GetResult().GetDisposition())
	}
}

func titles(opts []*pb.PanelOption) []string {
	var out []string
	for _, o := range opts {
		out = append(out, o.GetTitle())
	}
	return out
}

// The summary may explain the tally but not drop a dissent: code checks it
// names the top option and quotes every dissent's label, objections and
// would-change-mind word for word, asks again with what is missing, and
// keeps no summary that never passes.
func TestModeratorSummaryMustQuoteEveryDissent(t *testing.T) {
	a := func() *fakePanelist {
		return &fakePanelist{model: "a-1", phase1: proposals("Build it on gRPC", "Custom TCP framing"), phase2: ranking(90, "a binary stream already exists", "Build it on gRPC", "Custom TCP framing")}
	}
	b := func() *fakePanelist {
		return &fakePanelist{model: "b-1", phase1: proposals("Build it on gRPC"), phase2: ranking(60, "gRPC is banned", "Custom TCP framing", "Build it on gRPC")}
	}
	smoothed := jsonReply(panelSummary{Summary: "O3 wins; everyone broadly agrees."})

	// Smoothed over, then fixed on the retry.
	calls := 0
	mod := &fakeModerator{model: "m-1", frame: jsonReply(grpcFraming), merge: mergeAll("Build it on gRPC", "Custom TCP framing"), summary: func(req Request) (json.RawMessage, error) {
		calls++
		if calls == 1 {
			return smoothed(req)
		}
		if !strings.Contains(req.Turn, "gRPC is banned") || !strings.Contains(req.Turn, "Your previous answer was rejected") {
			return nil, fmt.Errorf("retry lacks the missing dissent: %q", req.Turn)
		}
		return quoteEverything(req)
	}}
	run, err := newModeratedPanel(t, mod, a(), b()).Run(context.Background(), acme, grpcQuestion)
	if err != nil {
		t.Fatal(err)
	}
	res := run.GetResult()
	for _, r := range mod.reqs {
		if strings.Contains(r.Turn, "openai-latest") || strings.Contains(r.Turn, "b-1") {
			t.Fatal("the summary request names the dissenting panelist")
		}
	}
	if calls != 2 || len(res.GetDissents()) != len(panelLenses) || !strings.Contains(res.GetSummary(), "gRPC is banned") || !strings.Contains(res.GetSummary(), "D5") {
		t.Fatalf("calls = %d summary = %q", calls, res.GetSummary())
	}
	if err := validSummary(panelSummary{Summary: res.GetSummary()}, res); err != nil {
		t.Fatalf("kept summary fails the check: %v", err)
	}

	// Smoothed over every time: no summary, a failure, the tally untouched.
	mod = &fakeModerator{model: "m-1", frame: jsonReply(grpcFraming), merge: mergeAll("Build it on gRPC", "Custom TCP framing"), summary: smoothed}
	run, err = newModeratedPanel(t, mod, a(), b()).Run(context.Background(), acme, grpcQuestion)
	if err != nil {
		t.Fatal(err)
	}
	if run.GetResult().GetSummary() != "" || len(run.GetResult().GetDissents()) != len(panelLenses) {
		t.Fatalf("a summary that drops dissent was kept: %q", run.GetResult().GetSummary())
	}
	last := run.GetFailures()[len(run.GetFailures())-1]
	if last.GetModeratorStep() != pb.PanelModeratorStep_PANEL_MODERATOR_STEP_SUMMARY {
		t.Fatalf("failures = %v", run.GetFailures())
	}
}

func TestValidSummary(t *testing.T) {
	res := &pb.PanelResult{TopOptionId: "O3", Dissents: []*pb.PanelDissent{
		{RankedFirst: "O4", WouldChangeMind: "gRPC is\nbanned", Objections: []*pb.PanelObjection{{OptionId: "O3", Text: "too weak"}}},
		{RankedFirst: "O1"},
	}}
	if err := validSummary(panelSummary{Summary: "O3 leads. D1 dissents: \"too weak\", \"gRPC is banned\". D2 prefers reuse."}, res); err != nil {
		t.Fatalf("valid summary refused: %v", err)
	}
	for name, s := range map[string]string{
		"no top option":     "D1: too weak, gRPC is banned. D2.",
		"a dissent dropped": "O3 leads. D1: too weak, gRPC is banned.",
		"D1 only as D10":    "O3 leads. D10: too weak, gRPC is banned. D2.",
		"a quote reworded":  "O3 leads. D1: too weak, gRPC is not allowed. D2.",
	} {
		var invalid *invalidPanelAnswer
		if err := validSummary(panelSummary{Summary: s}, res); !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v, want an invalid answer", name, err)
		}
	}
}

// recordingConn is a fake llmproxy connection: it keeps every request the
// generated client sends and answers each with the next of replies, then
// with reply.
type recordingConn struct {
	reply   *llmproxy_pb.GenerateCompletionResponse
	replies []*llmproxy_pb.GenerateCompletionResponse
	mu      sync.Mutex
	sent    []*llmproxy_pb.GenerateCompletionRequest
}

func (c *recordingConn) Invoke(_ context.Context, _ string, args, reply any, _ ...grpc.CallOption) error {
	c.mu.Lock()
	c.sent = append(c.sent, proto.Clone(args.(proto.Message)).(*llmproxy_pb.GenerateCompletionRequest))
	next := c.reply
	if len(c.replies) > 0 {
		next, c.replies = c.replies[0], c.replies[1:]
	}
	c.mu.Unlock()
	proto.Merge(reply.(proto.Message), next)
	return nil
}

func (c *recordingConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("no streams")
}

// On the wire, the three panelists' requests for the same input are
// byte-identical apart from the transport envelope, the alias llmproxy routes
// by. The model llmproxy resolved the alias to comes back with the answer.
func TestLlmProxyPanelRequestsDifferOnlyInAlias(t *testing.T) {
	conn := &recordingConn{reply: &llmproxy_pb.GenerateCompletionResponse{
		Result:        &llmproxy_pb.GenerateCompletionResponse_Text{Text: `{"options":[]}`},
		ProviderModel: "gpt-5.2",
	}}
	client := llmproxy_gc.NewLlmProxyClient(conn, func() (string, error) { return "token", nil })
	req := Request{System: panelPhase1Prompt, Turn: renderPanelPhase1(panelFramingOf(grpcQuestion)), Schema: panelOptionsSchema}
	aliases := []string{"claude-opus-latest", "openai-latest", "gemini-latest"}
	for _, alias := range aliases {
		raw, model, err := (&LlmProxy{Client: client, ModelAlias: alias}).CompleteWithModel(context.Background(), req)
		if err != nil || string(raw) != `{"options":[]}` || model != "gpt-5.2" {
			t.Fatalf("%s: %s %q %v", alias, raw, model, err)
		}
	}
	if len(conn.sent) != len(aliases) {
		t.Fatalf("sent %d requests", len(conn.sent))
	}
	var first []byte
	for i, sent := range conn.sent {
		if sent.GetModelAlias() != aliases[i] {
			t.Fatalf("request %d routed to %q", i, sent.GetModelAlias())
		}
		body := proto.Clone(sent).(*llmproxy_pb.GenerateCompletionRequest)
		body.ModelAlias = ""
		b, err := proto.MarshalOptions{Deterministic: true}.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = b
		} else if !bytes.Equal(b, first) {
			t.Fatalf("%s's request differs from %s's beyond the alias:\n%v\n%v", aliases[i], aliases[0], body, conn.sent[0])
		}
	}
}

// The config delivery builds the moderator as its own instance, apart from
// any seat on the same alias.
func TestServerConfigBuildsModerator(t *testing.T) {
	rec := newFakeRecords()
	var built []Panelist
	s := &Server{
		Records: rec, Publish: rec.publish, PublishSession: rec.publishSession, PublishPanelRun: rec.publishPanelRun,
		NewProvider: func(string) Provider { return &scriptedProvider{} },
		NewPanelist: func(alias string) Panelist { f := &fakePanelist{model: alias}; built = append(built, f); return f },
	}
	cfg := &pb.Config{ModelAlias: "m", PanelModelAliases: []string{"claude-opus-latest", "openai-latest", "gemini-latest"},
		PanelModeratorAlias: "claude-opus-latest", PrinciplesPaths: []string{"principles/"}}
	if err := s.DoppelgangerConfig(cfg); err != nil {
		t.Fatal(err)
	}
	panel, _ := s.currentPanel()
	if panel.Moderator == nil || panel.Moderator.Alias != "claude-opus-latest" || len(built) != 4 {
		t.Fatalf("moderator = %+v built = %d", panel.Moderator, len(built))
	}
	if panel.Moderator.Panelist == panel.Seats[0].Panelist {
		t.Fatal("the moderator shares an instance with panelist #1")
	}
	cfg.PanelModeratorAlias = ""
	if err := s.DoppelgangerConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if panel, _ := s.currentPanel(); panel.Moderator != nil {
		t.Fatal("no moderator alias must mean no moderator")
	}
}
