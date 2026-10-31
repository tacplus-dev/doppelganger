// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
)

//go:embed panel_phase1.md
var panelPhase1Prompt string

//go:embed panel_phase2.md
var panelPhase2Prompt string

// Panelist is one panel seat's model, reached through llmproxy by an alias. It
// turns one Request into the model's JSON answer and names the model that gave
// it (empty while llmproxy does not report it, tacplus-dev/llmproxy#13), so
// every answer is scored against the model that actually answered.
type Panelist interface {
	CompleteWithModel(ctx context.Context, req Request) (json.RawMessage, string, error)
}

// PanelSeat is one member of the panel: the llmproxy alias it is asked by and
// the provider behind it.
type PanelSeat struct {
	Alias    string
	Panelist Panelist
}

// Panel runs panel mode (#10). Phase 1 asks every seat for alternatives from
// the question and the situation alone; code merges them. Phase 2 asks every
// seat under every lens to rank them from the person's principles; code
// tallies. Nothing from the context but the principles files reaches a seat.
type Panel struct {
	Contexts        *ContextStore
	Seats           []PanelSeat
	PrinciplesPaths []string
	now             func() time.Time
}

// PanelQuestion is what a caller asks the panel.
type PanelQuestion struct {
	Question                  string
	Situation                 string
	Person                    string
	Kind                      string
	Reversible                bool
	TouchesSecurityOrIdentity bool
}

var (
	errNoPanel      = errors.New("no panel configured: panel_model_aliases is empty")
	errNoPrinciples = errors.New("no principles: principles_paths matches no Markdown file of the tenant's context")
)

// The two options every panel list carries (tacplus-dev/roadmap
// decisions/issue-flow-2026-10-27.md, addendum 2026-10-31).
const (
	optionReuse = "Reuse what the platform already has"
	optionDefer = "Do nothing / defer"
)

// maxPanelOptions bounds one seat's proposals; the merged list is bounded by
// the record (64).
const maxPanelOptions = 8

type panelLens struct {
	lens        pb.PanelLens
	instruction string
}

// panelLenses are the angles every seat answers from, in order.
var panelLenses = []panelLens{
	{pb.PanelLens_PANEL_LENS_REUSE_AND_GENERATOR, "Reuse and generator: prefer what already exists, and what a generator produces over what is written by hand."},
	{pb.PanelLens_PANEL_LENS_INVARIANTS, "Invariants: expiry, revocation, fail closed, no mTLS, reachability is not authorization."},
	{pb.PanelLens_PANEL_LENS_FAILURE_OTHER_SIDE, "Failure as seen from the other side: how each option fails for the peer, the caller or the operator, not only for the one who builds it."},
	{pb.PanelLens_PANEL_LENS_PRODUCER_CONSUMER, "Producer vs consumer: who owns the contract, who depends on it, and what each side must know."},
	{pb.PanelLens_PANEL_LENS_ADVERSARY, "Adversary: what would the person object to? Look for the objection first, then rank."},
}

type panelProposal struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

type panelProposals struct {
	Options []panelProposal `json:"options"`
}

const panelOptionsSchema = `{
  "type": "object",
  "properties": {
    "options": {
      "type": "array",
      "maxItems": 8,
      "items": {
        "type": "object",
        "properties": {"title": {"type": "string"}, "summary": {"type": "string"}},
        "required": ["title", "summary"]
      }
    }
  },
  "required": ["options"]
}`

type panelObjection struct {
	OptionID string `json:"option_id"`
	Text     string `json:"text"`
}

type panelRanking struct {
	Ranked          []string         `json:"ranked_option_ids"`
	Confidence      int              `json:"confidence"`
	Objections      []panelObjection `json:"objections"`
	WouldChangeMind string           `json:"would_change_mind"`
}

const panelRankingSchema = `{
  "type": "object",
  "properties": {
    "ranked_option_ids": {"type": "array", "items": {"type": "string"}},
    "confidence": {"type": "integer", "minimum": 0, "maximum": 100},
    "objections": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {"option_id": {"type": "string"}, "text": {"type": "string"}},
        "required": ["option_id", "text"]
      }
    },
    "would_change_mind": {"type": "string"}
  },
  "required": ["ranked_option_ids", "confidence", "objections", "would_change_mind"]
}`

// Run asks the panel one question. It fails closed before any model call when
// there is no panel or no principles, and when no seat gives a usable answer;
// a seat that fails is reported in the run's failures, never counted.
func (p *Panel) Run(ctx context.Context, tenant string, q PanelQuestion) (*pb.PanelRun, error) {
	if len(p.Seats) == 0 {
		return nil, errNoPanel
	}
	if q.Person == "" {
		q.Person = "Jody"
	}
	principles, err := p.Contexts.Principles(ctx, tenant, p.PrinciplesPaths)
	if err != nil {
		return nil, err
	}
	run := &pb.PanelRun{Person: q.Person, QuestionKind: q.Kind, Question: q.Question, Situation: q.Situation}

	// Phase 1: alternatives, from the question and the situation only.
	props := make([][]panelProposal, len(p.Seats))
	models := make([]*pb.PanelModel, len(p.Seats))
	errs := make([]error, len(p.Seats))
	p.each(len(p.Seats), func(i int) {
		req := Request{System: panelPhase1Prompt, Turn: renderPanelPhase1(q), Schema: panelOptionsSchema}
		var out panelProposals
		models[i], errs[i] = p.ask(ctx, p.Seats[i], req, func(raw json.RawMessage) error {
			out = panelProposals{}
			if err := json.Unmarshal(raw, &out); err != nil {
				return fmt.Errorf("%w: %v", errStructuredOutput, err)
			}
			return validProposals(out)
		})
		props[i] = out.Options
	})
	var proposers []*pb.PanelModel
	var proposed [][]panelProposal
	for i := range p.Seats {
		if errs[i] != nil {
			run.Failures = append(run.Failures, &pb.PanelFailure{Model: models[i], Phase: 1, Error: errs[i].Error()})
			continue
		}
		proposers = append(proposers, models[i])
		proposed = append(proposed, props[i])
	}
	if len(proposers) == 0 {
		return nil, fmt.Errorf("no panelist listed alternatives: %v", errors.Join(errs...))
	}
	run.Options = mergeOptions(proposers, proposed)

	// Phase 2: every seat that answered phase 1, under every lens.
	type job struct {
		seat PanelSeat
		lens panelLens
	}
	var jobs []job
	for i, s := range p.Seats {
		if errs[i] == nil {
			for _, l := range panelLenses {
				jobs = append(jobs, job{s, l})
			}
		}
	}
	known := map[string]bool{}
	for _, o := range run.Options {
		known[o.GetOptionId()] = true
	}
	answers := make([]*pb.PanelAnswer, len(jobs))
	failures := make([]*pb.PanelFailure, len(jobs))
	p.each(len(jobs), func(i int) {
		j := jobs[i]
		req := Request{System: panelPhase2Prompt, Turn: renderPanelPhase2(q, run.Options, principles, j.lens), Schema: panelRankingSchema}
		var out panelRanking
		model, err := p.ask(ctx, j.seat, req, func(raw json.RawMessage) error {
			out = panelRanking{}
			if err := json.Unmarshal(raw, &out); err != nil {
				return fmt.Errorf("%w: %v", errStructuredOutput, err)
			}
			return validRanking(out, known)
		})
		if err != nil {
			failures[i] = &pb.PanelFailure{Model: model, Phase: 2, Lens: j.lens.lens, Error: err.Error()}
			return
		}
		ans := &pb.PanelAnswer{
			Model:           model,
			Lens:            j.lens.lens,
			RankedOptionIds: out.Ranked,
			Confidence:      int32(out.Confidence),
			WouldChangeMind: out.WouldChangeMind,
			AnsweredAtMs:    p.clock().UnixMilli(),
		}
		for _, o := range out.Objections {
			ans.Objections = append(ans.Objections, &pb.PanelObjection{OptionId: o.OptionID, Text: o.Text})
		}
		answers[i] = ans
	})
	for i := range jobs {
		if answers[i] != nil {
			run.Answers = append(run.Answers, answers[i])
		} else {
			run.Failures = append(run.Failures, failures[i])
		}
	}
	if len(run.Answers) == 0 {
		return nil, errors.New("no panelist gave a usable ranking")
	}
	run.Result = tallyPanel(run.Options, run.Answers, q, len(run.Failures))
	return run, nil
}

// ask makes one seat's call, retrying when accept refuses the reply as not
// the schema (errStructuredOutput) or as breaking its rules (told the reason),
// up to maxAttempts. It returns the model that answered last, so a failure is
// reported against it too.
func (p *Panel) ask(ctx context.Context, seat PanelSeat, req Request, accept func(json.RawMessage) error) (*pb.PanelModel, error) {
	model := &pb.PanelModel{ModelAlias: seat.Alias}
	var last error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		raw, resolved, err := seat.Panelist.CompleteWithModel(ctx, req)
		model.ResolvedModel = resolved
		if err == nil {
			err = accept(raw)
		}
		if err == nil {
			return model, nil
		}
		last = err
		var invalid *invalidPanelAnswer
		switch {
		case errors.As(err, &invalid):
			req.Turn += "\n# Your previous answer was rejected\n\n" + invalid.reason + "\nAnswer the same question again.\n"
		case errors.Is(err, errStructuredOutput):
		default:
			return model, err
		}
	}
	return model, fmt.Errorf("no acceptable answer after %d attempts: %w", maxAttempts, last)
}

func (p *Panel) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// each runs f(0..n-1) concurrently and waits; f writes only its own index.
func (p *Panel) each(n int, f func(i int)) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f(i)
		}()
	}
	wg.Wait()
}

// invalidPanelAnswer is a reply in the schema's shape that breaks its rules:
// the seat is told why and asked again.
type invalidPanelAnswer struct{ reason string }

func (e *invalidPanelAnswer) Error() string { return "invalid panel answer: " + e.reason }

func validProposals(p panelProposals) error {
	if len(p.Options) > maxPanelOptions {
		return &invalidPanelAnswer{fmt.Sprintf("it listed %d options; list at most %d.", len(p.Options), maxPanelOptions)}
	}
	for _, o := range p.Options {
		if normalizeOptionTitle(o.Title) == "" {
			return &invalidPanelAnswer{"an option has no title."}
		}
	}
	return nil
}

// validRanking holds a ranking to the schema's rules that a JSON Schema alone
// cannot: only the run's option ids, each once, a confidence from 0 to 100,
// objections to options that exist.
func validRanking(r panelRanking, known map[string]bool) error {
	if len(r.Ranked) == 0 {
		return &invalidPanelAnswer{"it ranks no option."}
	}
	seen := map[string]bool{}
	for _, id := range r.Ranked {
		if !known[id] {
			return &invalidPanelAnswer{fmt.Sprintf("it ranks %q, an unknown option; use the ids given.", id)}
		}
		if seen[id] {
			return &invalidPanelAnswer{fmt.Sprintf("it ranks %q twice.", id)}
		}
		seen[id] = true
	}
	if r.Confidence < 0 || r.Confidence > 100 {
		return &invalidPanelAnswer{fmt.Sprintf("its confidence %d is not from 0 to 100.", r.Confidence)}
	}
	for _, o := range r.Objections {
		if !known[o.OptionID] {
			return &invalidPanelAnswer{fmt.Sprintf("an objection names %q, an unknown option.", o.OptionID)}
		}
	}
	return nil
}

// normalizeOptionTitle is the merge key: lower case, letters and digits only,
// single spaces. Options merge only when their keys are equal; merging by
// meaning would need a model, and its output a code check (#10, decision 6).
func normalizeOptionTitle(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
			continue
		}
		space = true
	}
	return b.String()
}

// mergeOptions merges the seats' proposals by normalized title, in the order
// first proposed, after the two fixed options. Every option keeps every seat
// that proposed it; its summary is the first proposer's.
func mergeOptions(models []*pb.PanelModel, proposals [][]panelProposal) []*pb.PanelOption {
	opts := []*pb.PanelOption{
		{Title: optionReuse, Summary: "Build nothing new: use what the platform already provides.", Fixed: true},
		{Title: optionDefer, Summary: "Change nothing now; decide later, if at all.", Fixed: true},
	}
	byKey := map[string]*pb.PanelOption{}
	for _, o := range opts {
		byKey[normalizeOptionTitle(o.GetTitle())] = o
	}
	for i, props := range proposals {
		for _, prop := range props {
			key := normalizeOptionTitle(prop.Title)
			o, ok := byKey[key]
			if !ok {
				o = &pb.PanelOption{Title: strings.TrimSpace(prop.Title), Summary: prop.Summary}
				byKey[key] = o
				opts = append(opts, o)
			}
			if !proposedBy(o, models[i]) {
				o.ProposedBy = append(o.ProposedBy, models[i])
			}
		}
	}
	for i, o := range opts {
		o.OptionId = fmt.Sprintf("O%d", i+1)
	}
	return opts
}

func proposedBy(o *pb.PanelOption, m *pb.PanelModel) bool {
	for _, p := range o.GetProposedBy() {
		if p.GetModelAlias() == m.GetModelAlias() {
			return true
		}
	}
	return false
}

// tallyPanel is the plain-code count: Borda points and first places per
// option, the top option (most points, then most first places, then the
// earlier option), agreement as the share of answers that ranked it first,
// every answer that did not as a dissent, and every objection to it and every
// would-change-mind word for word. The same answers always tally the same.
func tallyPanel(options []*pb.PanelOption, answers []*pb.PanelAnswer, q PanelQuestion, failures int) *pb.PanelResult {
	n := int32(len(options))
	tallies := make([]*pb.PanelOptionTally, len(options))
	byID := map[string]*pb.PanelOptionTally{}
	for i, o := range options {
		tallies[i] = &pb.PanelOptionTally{OptionId: o.GetOptionId()}
		byID[o.GetOptionId()] = tallies[i]
	}
	for _, a := range answers {
		for r, id := range a.GetRankedOptionIds() {
			byID[id].Points += n - int32(r)
		}
		byID[a.GetRankedOptionIds()[0]].FirstPlace++
	}
	top := tallies[0]
	for _, t := range tallies[1:] {
		if t.Points > top.Points || (t.Points == top.Points && t.FirstPlace > top.FirstPlace) {
			top = t
		}
	}
	res := &pb.PanelResult{
		Provenance:  pb.Provenance_PROVENANCE_DERIVED_PANEL,
		TopOptionId: top.GetOptionId(),
		Agreement:   top.GetFirstPlace() * 100 / int32(len(answers)),
		Tallies:     tallies,
	}
	for _, a := range answers {
		if first := a.GetRankedOptionIds()[0]; first != top.GetOptionId() {
			res.Dissents = append(res.Dissents, &pb.PanelDissent{
				Model: a.GetModel(), Lens: a.GetLens(), RankedFirst: first,
				Objections: a.GetObjections(), WouldChangeMind: a.GetWouldChangeMind(),
			})
		}
		for _, o := range a.GetObjections() {
			if o.GetOptionId() == top.GetOptionId() {
				res.ObjectionsToTop = append(res.ObjectionsToTop, o)
			}
		}
		if a.GetWouldChangeMind() != "" {
			res.WouldChangeMind = append(res.WouldChangeMind, a.GetWouldChangeMind())
		}
	}
	// Strong agreement on a reversible matter may be acted on; dissent, a
	// missing panelist, or security or identity goes to the person.
	res.Disposition = pb.PanelDisposition_PANEL_DISPOSITION_TO_PERSON
	if len(res.Dissents) == 0 && failures == 0 && q.Reversible && !q.TouchesSecurityOrIdentity {
		res.Disposition = pb.PanelDisposition_PANEL_DISPOSITION_MAY_ACT
	}
	return res
}

func renderPanelPhase1(q PanelQuestion) string {
	return "# Question\n\n" + q.Question + "\n\n# Requirements and situation\n\n" + q.Situation + "\n"
}

func renderPanelPhase2(q PanelQuestion, options []*pb.PanelOption, principles string, lens panelLens) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Whose call\n\n%s\n\n", q.Person)
	b.WriteString("# Question\n\n" + q.Question + "\n\n# Requirements and situation\n\n" + q.Situation + "\n\n")
	b.WriteString("# Options\n\n")
	for _, o := range options {
		fmt.Fprintf(&b, "- %s: %s\n  %s\n", o.GetOptionId(), o.GetTitle(), strings.ReplaceAll(o.GetSummary(), "\n", "\n  "))
	}
	fmt.Fprintf(&b, "\n# %s's principles\n\n%s\n", q.Person, principles)
	b.WriteString("# Your lens\n\n" + lens.instruction + "\n")
	return b.String()
}

// panelTrackRecord scores decided runs per (model, question kind): a model is
// its alias and the resolved id, so a new model is a new row.
func panelTrackRecord(runs []*pb.PanelRun) *pb.GetPanelTrackRecordResponse {
	type key struct{ alias, model, kind string }
	type acc struct {
		runs      map[string]bool
		answers   int32
		hits      int32
		rankTotal int
	}
	byKey := map[key]*acc{}
	resp := &pb.GetPanelTrackRecordResponse{}
	for _, run := range runs {
		decided := run.GetDecidedOptionId()
		if decided == "" {
			continue
		}
		resp.DecidedRuns++
		for _, a := range run.GetAnswers() {
			k := key{a.GetModel().GetModelAlias(), a.GetModel().GetResolvedModel(), run.GetQuestionKind()}
			c := byKey[k]
			if c == nil {
				c = &acc{runs: map[string]bool{}}
				byKey[k] = c
			}
			c.runs[run.GetPanelId()] = true
			c.answers++
			rank := len(a.GetRankedOptionIds()) + 1
			for i, id := range a.GetRankedOptionIds() {
				if id == decided {
					rank = i + 1
					break
				}
			}
			if rank == 1 {
				c.hits++
			}
			c.rankTotal += rank
		}
	}
	keys := make([]key, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		if a.alias != b.alias {
			return a.alias < b.alias
		}
		return a.model < b.model
	})
	for _, k := range keys {
		c := byKey[k]
		resp.Records = append(resp.Records, &pb.PanelTrackRecord{
			Model:        &pb.PanelModel{ModelAlias: k.alias, ResolvedModel: k.model},
			QuestionKind: k.kind,
			Runs:         int32(len(c.runs)),
			Answers:      c.answers,
			TopHits:      c.hits,
			MeanRank:     float64(c.rankTotal) / float64(c.answers),
		})
	}
	return resp
}
