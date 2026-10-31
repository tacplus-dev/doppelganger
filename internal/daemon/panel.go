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

//go:embed panel_frame.md
var panelFramePrompt string

//go:embed panel_merge.md
var panelMergePrompt string

//go:embed panel_summary.md
var panelSummaryPrompt string

// Panelist is one panel seat's model, reached through llmproxy by an alias. It
// turns one Request into the model's JSON answer and names the model that gave
// it (the model llmproxy resolved the alias to), so every answer is scored
// against the model that actually answered.
type Panelist interface {
	CompleteWithModel(ctx context.Context, req Request) (json.RawMessage, string, error)
}

// PanelSeat is one member of the panel: the llmproxy alias it is asked by and
// the provider behind it.
type PanelSeat struct {
	Alias    string
	Panelist Panelist
}

// Panel runs panel mode (#10). The moderator, if any, frames the question.
// Phase 1 asks every seat for alternatives from the question and the
// situation alone; the moderator merges them, checked by code, or code does.
// Phase 2 asks every seat under every lens to rank them from the person's
// principles; code tallies, and the moderator explains the tally, checked by
// code to quote every dissent. Nothing from the context but the principles
// files reaches a seat, and nothing at all reaches the moderator.
//
// Every seat is given the same requests, byte for byte (Jody, 2026-10-31): a
// seat on the moderator's alias is a separate instance, and no moderator call
// feeds a seat's beyond the framing and the merged options every seat gets.
type Panel struct {
	Contexts        *ContextStore
	Seats           []PanelSeat
	Moderator       *PanelSeat // nil: no moderator
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

// panelFraming is the question as every seat is given it.
type panelFraming struct {
	Question     string   `json:"question"`
	Requirements []string `json:"requirements"`
	Situation    string   `json:"situation"`
}

const panelFrameSchema = `{
  "type": "object",
  "properties": {
    "question": {"type": "string"},
    "requirements": {"type": "array", "maxItems": 64, "items": {"type": "string"}},
    "situation": {"type": "string"}
  },
  "required": ["question", "requirements", "situation"]
}`

type panelMergedOption struct {
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Members []string `json:"members"`
}

type panelMerged struct {
	Options []panelMergedOption `json:"options"`
}

const panelMergeSchema = `{
  "type": "object",
  "properties": {
    "options": {
      "type": "array",
      "maxItems": 64,
      "items": {
        "type": "object",
        "properties": {
          "title": {"type": "string"},
          "summary": {"type": "string"},
          "members": {"type": "array", "items": {"type": "string"}}
        },
        "required": ["title", "summary", "members"]
      }
    }
  },
  "required": ["options"]
}`

type panelSummary struct {
	Summary string `json:"summary"`
}

const panelSummarySchema = `{
  "type": "object",
  "properties": {"summary": {"type": "string"}},
  "required": ["summary"]
}`

// Run asks the panel one question. It fails closed before any model call when
// there is no panel or no principles, and when no seat gives a usable answer;
// a seat that fails is reported in the run's failures, never counted. A
// moderator step that fails falls back to code and is reported too; it never
// changes what is counted.
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
	framing := p.frame(ctx, run, q)
	run.FramedQuestion, run.FramedRequirements, run.FramedSituation = framing.Question, framing.Requirements, framing.Situation

	// Phase 1: alternatives, from the question and the situation only.
	props := make([][]panelProposal, len(p.Seats))
	models := make([]*pb.PanelModel, len(p.Seats))
	errs := make([]error, len(p.Seats))
	phase1 := Request{System: panelPhase1Prompt, Turn: renderPanelPhase1(framing), Schema: panelOptionsSchema}
	p.each(len(p.Seats), func(i int) {
		req := phase1
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
	run.Options = p.merge(ctx, run, framing, proposers, proposed)

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
		req := Request{System: panelPhase2Prompt, Turn: renderPanelPhase2(q.Person, framing, run.Options, principles, j.lens), Schema: panelRankingSchema}
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
	panelists := 0
	for _, f := range run.Failures {
		if f.GetModeratorStep() == pb.PanelModeratorStep_PANEL_MODERATOR_STEP_UNSPECIFIED {
			panelists++
		}
	}
	run.Result = tallyPanel(run.Options, run.Answers, q, panelists)
	run.Result.Summary = p.summarize(ctx, run, framing)
	return run, nil
}

// panelFramingOf is the caller's question and situation as given.
func panelFramingOf(q PanelQuestion) panelFraming {
	return panelFraming{Question: q.Question, Situation: q.Situation}
}

// frame asks the moderator to frame the question for phase 1, from the
// caller's question and situation alone; without a moderator, or when it
// fails, the caller's text is used as given.
func (p *Panel) frame(ctx context.Context, run *pb.PanelRun, q PanelQuestion) panelFraming {
	given := panelFramingOf(q)
	if p.Moderator == nil {
		return given
	}
	req := Request{System: panelFramePrompt, Turn: renderPanelPhase1(given), Schema: panelFrameSchema}
	var out panelFraming
	model, err := p.ask(ctx, *p.Moderator, req, func(raw json.RawMessage) error {
		out = panelFraming{}
		if err := json.Unmarshal(raw, &out); err != nil {
			return fmt.Errorf("%w: %v", errStructuredOutput, err)
		}
		return validFraming(out)
	})
	run.Moderator = model
	if err != nil {
		p.moderatorFailed(run, model, pb.PanelModeratorStep_PANEL_MODERATOR_STEP_FRAME, err)
		return given
	}
	out.Question, out.Situation = strings.TrimSpace(out.Question), strings.TrimSpace(out.Situation)
	for i, r := range out.Requirements {
		out.Requirements[i] = strings.TrimSpace(r)
	}
	return out
}

// merge asks the moderator to merge the phase-1 proposals, shown without
// their proposers, and builds the options from its grouping, which code has
// checked; proposers and proposals are kept by code. Without a moderator, or
// when it fails, code merges by normalized title.
func (p *Panel) merge(ctx context.Context, run *pb.PanelRun, framing panelFraming, models []*pb.PanelModel, proposals [][]panelProposal) []*pb.PanelOption {
	if p.Moderator == nil {
		return mergeOptions(models, proposals)
	}
	type member struct {
		model *pb.PanelModel
		prop  panelProposal
	}
	ids := []string{"F1", "F2"}
	members := map[string]member{}
	for i, props := range proposals {
		for _, prop := range props {
			id := fmt.Sprintf("P%d", len(members)+1)
			ids = append(ids, id)
			members[id] = member{models[i], prop}
		}
	}
	req := Request{System: panelMergePrompt, Turn: renderPanelMerge(framing, ids, func(id string) panelProposal { return members[id].prop }), Schema: panelMergeSchema}
	var out panelMerged
	model, err := p.ask(ctx, *p.Moderator, req, func(raw json.RawMessage) error {
		out = panelMerged{}
		if err := json.Unmarshal(raw, &out); err != nil {
			return fmt.Errorf("%w: %v", errStructuredOutput, err)
		}
		return validMerge(out, ids)
	})
	run.Moderator = model
	if err != nil {
		p.moderatorFailed(run, model, pb.PanelModeratorStep_PANEL_MODERATOR_STEP_MERGE, err)
		return mergeOptions(models, proposals)
	}
	reuse, deferred := fixedOptions()
	opts := []*pb.PanelOption{reuse, deferred}
	for _, g := range out.Options {
		var o *pb.PanelOption
		switch {
		case contains(g.Members, "F1"):
			o = reuse
		case contains(g.Members, "F2"):
			o = deferred
		default:
			o = &pb.PanelOption{Title: strings.TrimSpace(g.Title), Summary: strings.TrimSpace(g.Summary)}
			opts = append(opts, o)
		}
		ms := append([]string{}, g.Members...)
		sort.Slice(ms, func(i, j int) bool { return proposalIndex(ms[i]) < proposalIndex(ms[j]) })
		for _, id := range ms {
			m, ok := members[id]
			if !ok {
				continue // F1 or F2
			}
			addProposal(o, m.model, m.prop)
		}
	}
	numberOptions(opts)
	run.ModeratorMerged = true
	return opts
}

// summarize asks the moderator to explain the tally for the person, and
// keeps the summary only if code finds it names the top option and quotes
// every dissent. Without a moderator, or when it fails, there is none.
func (p *Panel) summarize(ctx context.Context, run *pb.PanelRun, framing panelFraming) string {
	if p.Moderator == nil {
		return ""
	}
	req := Request{System: panelSummaryPrompt, Turn: renderPanelSummary(framing, run.Options, run.Result), Schema: panelSummarySchema}
	var out panelSummary
	model, err := p.ask(ctx, *p.Moderator, req, func(raw json.RawMessage) error {
		out = panelSummary{}
		if err := json.Unmarshal(raw, &out); err != nil {
			return fmt.Errorf("%w: %v", errStructuredOutput, err)
		}
		return validSummary(out, run.Result)
	})
	run.Moderator = model
	if err != nil {
		p.moderatorFailed(run, model, pb.PanelModeratorStep_PANEL_MODERATOR_STEP_SUMMARY, err)
		return ""
	}
	return strings.TrimSpace(out.Summary)
}

func (p *Panel) moderatorFailed(run *pb.PanelRun, model *pb.PanelModel, step pb.PanelModeratorStep, err error) {
	run.Failures = append(run.Failures, &pb.PanelFailure{Model: model, Phase: 0, ModeratorStep: step, Error: err.Error()})
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

func validFraming(f panelFraming) error {
	if strings.TrimSpace(f.Question) == "" {
		return &invalidPanelAnswer{"the question is empty."}
	}
	if strings.TrimSpace(f.Situation) == "" {
		return &invalidPanelAnswer{"the situation is empty."}
	}
	if len(f.Requirements) > 64 {
		return &invalidPanelAnswer{fmt.Sprintf("it lists %d requirements; list at most 64.", len(f.Requirements))}
	}
	for _, r := range f.Requirements {
		if strings.TrimSpace(r) == "" {
			return &invalidPanelAnswer{"a requirement is empty."}
		}
	}
	return nil
}

// validMerge is the code check on the moderator's merge (#10, decision 6):
// every input id (F1, F2, every P) in exactly one option, no option without
// members (none invented), the two fixed options apart, every option titled.
func validMerge(m panelMerged, ids []string) error {
	if len(m.Options) > 64 {
		return &invalidPanelAnswer{fmt.Sprintf("it gives %d options; give at most 64.", len(m.Options))}
	}
	known := map[string]bool{}
	for _, id := range ids {
		known[id] = true
	}
	in := map[string]int{}
	for i, o := range m.Options {
		if strings.TrimSpace(o.Title) == "" {
			return &invalidPanelAnswer{fmt.Sprintf("option %d has no title.", i+1)}
		}
		if len(o.Members) == 0 {
			return &invalidPanelAnswer{fmt.Sprintf("option %q has no members; do not invent options.", o.Title)}
		}
		for _, id := range o.Members {
			if !known[id] {
				return &invalidPanelAnswer{fmt.Sprintf("option %q names %q, an unknown id; use the ids given.", o.Title, id)}
			}
			if j, ok := in[id]; ok {
				return &invalidPanelAnswer{fmt.Sprintf("%s is in option %d and option %d; put each id in exactly one.", id, j+1, i+1)}
			}
			in[id] = i
		}
	}
	var missing []string
	for _, id := range ids {
		if _, ok := in[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return &invalidPanelAnswer{fmt.Sprintf("%s in no option; put every id in exactly one.", strings.Join(missing, ", "))}
	}
	if in["F1"] == in["F2"] {
		return &invalidPanelAnswer{"F1 and F2 are in the same option; keep them apart."}
	}
	return nil
}

// validSummary is the code check on the moderator's summary: it names the
// top option and every dissent by its label (D1, D2, ... in the tally's
// order), and quotes each dissent's objections and would-change-mind word for
// word (whitespace aside). The tally itself is never read back from it.
func validSummary(s panelSummary, res *pb.PanelResult) error {
	text := collapseSpace(s.Summary)
	if !hasToken(text, res.GetTopOptionId()) {
		return &invalidPanelAnswer{fmt.Sprintf("it does not name the top option, %s.", res.GetTopOptionId())}
	}
	var missing []string
	for i, d := range res.GetDissents() {
		label := fmt.Sprintf("D%d", i+1)
		if !hasToken(text, label) {
			missing = append(missing, label)
		}
		for _, q := range dissentQuotes(d) {
			if !strings.Contains(text, collapseSpace(q)) {
				missing = append(missing, fmt.Sprintf("%s's %q", label, q))
			}
		}
	}
	if len(missing) > 0 {
		return &invalidPanelAnswer{"it leaves out or rewords dissent; quote word for word: " + strings.Join(missing, "; ") + "."}
	}
	return nil
}

// dissentQuotes are the texts of a dissent the summary must quote.
func dissentQuotes(d *pb.PanelDissent) []string {
	var qs []string
	for _, o := range d.GetObjections() {
		if strings.TrimSpace(o.GetText()) != "" {
			qs = append(qs, o.GetText())
		}
	}
	if strings.TrimSpace(d.GetWouldChangeMind()) != "" {
		qs = append(qs, d.GetWouldChangeMind())
	}
	return qs
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// hasToken says whether word occurs in s on its own, not inside a longer
// word or number (D1 is not in D10).
func hasToken(s, word string) bool {
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(word)
		before, after := ' ', ' '
		if start > 0 {
			before = rune(s[start-1])
		}
		if end < len(s) {
			after = rune(s[end])
		}
		if !isWord(before) && !isWord(after) {
			return true
		}
		i = start + 1
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// proposalIndex orders P1, P2, ... by number (seat order), F ids first.
func proposalIndex(id string) int {
	var n int
	if _, err := fmt.Sscanf(id, "P%d", &n); err != nil {
		return 0
	}
	return n
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
	reuse, deferred := fixedOptions()
	opts := []*pb.PanelOption{reuse, deferred}
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
			addProposal(o, models[i], prop)
		}
	}
	numberOptions(opts)
	return opts
}

// fixedOptions are the two options every list carries, first.
func fixedOptions() (reuse, deferred *pb.PanelOption) {
	return &pb.PanelOption{Title: optionReuse, Summary: "Build nothing new: use what the platform already provides.", Fixed: true},
		&pb.PanelOption{Title: optionDefer, Summary: "Change nothing now; decide later, if at all.", Fixed: true}
}

// addProposal keeps a proposal, word for word, on the option it merged into,
// and its panelist among the option's proposers.
func addProposal(o *pb.PanelOption, m *pb.PanelModel, prop panelProposal) {
	o.Proposals = append(o.Proposals, &pb.PanelProposal{Model: m, Title: prop.Title, Summary: prop.Summary})
	if !proposedBy(o, m) {
		o.ProposedBy = append(o.ProposedBy, m)
	}
}

func numberOptions(opts []*pb.PanelOption) {
	for i, o := range opts {
		o.OptionId = fmt.Sprintf("O%d", i+1)
	}
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

// renderFraming is the question, then the requirements (when the moderator
// listed them) and the situation.
func renderFraming(f panelFraming) string {
	var b strings.Builder
	b.WriteString("# Question\n\n" + f.Question + "\n\n")
	if len(f.Requirements) == 0 {
		b.WriteString("# Requirements and situation\n\n" + f.Situation + "\n")
		return b.String()
	}
	b.WriteString("# Requirements\n\n")
	for _, r := range f.Requirements {
		b.WriteString("- " + r + "\n")
	}
	b.WriteString("\n# Situation\n\n" + f.Situation + "\n")
	return b.String()
}

func renderPanelPhase1(f panelFraming) string { return renderFraming(f) }

func renderPanelPhase2(person string, f panelFraming, options []*pb.PanelOption, principles string, lens panelLens) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Whose call\n\n%s\n\n", person)
	b.WriteString(renderFraming(f) + "\n")
	b.WriteString("# Options\n\n")
	for _, o := range options {
		fmt.Fprintf(&b, "- %s: %s\n  %s\n", o.GetOptionId(), o.GetTitle(), indent(o.GetSummary()))
	}
	fmt.Fprintf(&b, "\n# %s's principles\n\n%s\n", person, principles)
	b.WriteString("# Your lens\n\n" + lens.instruction + "\n")
	return b.String()
}

func indent(s string) string { return strings.ReplaceAll(s, "\n", "\n  ") }

// renderPanelMerge shows the moderator the fixed options and every proposal
// by id, without who proposed it.
func renderPanelMerge(f panelFraming, ids []string, proposal func(id string) panelProposal) string {
	var b strings.Builder
	b.WriteString(renderFraming(f) + "\n# Options to merge\n\n")
	reuse, deferred := fixedOptions()
	fmt.Fprintf(&b, "- F1: %s\n  (fixed) %s\n", reuse.GetTitle(), reuse.GetSummary())
	fmt.Fprintf(&b, "- F2: %s\n  (fixed) %s\n", deferred.GetTitle(), deferred.GetSummary())
	for _, id := range ids[2:] {
		prop := proposal(id)
		fmt.Fprintf(&b, "- %s: %s\n  %s\n", id, strings.TrimSpace(prop.Title), indent(prop.Summary))
	}
	return b.String()
}

// renderPanelSummary shows the moderator the tally and every dissent by
// label, with the texts it must quote. It names no person, no principle and
// no panelist, so the moderator cannot tell its own model's dissent from
// another's; the result carries each dissent's model.
func renderPanelSummary(f panelFraming, options []*pb.PanelOption, res *pb.PanelResult) string {
	var b strings.Builder
	b.WriteString("# Question\n\n" + f.Question + "\n\n# Options and tally\n\n")
	byID := map[string]*pb.PanelOptionTally{}
	for _, t := range res.GetTallies() {
		byID[t.GetOptionId()] = t
	}
	for _, o := range options {
		t := byID[o.GetOptionId()]
		fmt.Fprintf(&b, "- %s: %s (points %d, first places %d)\n", o.GetOptionId(), o.GetTitle(), t.GetPoints(), t.GetFirstPlace())
	}
	fmt.Fprintf(&b, "\nTop option: %s. Agreement: %d%% of answers ranked it first. Disposition: %s.\n",
		res.GetTopOptionId(), res.GetAgreement(), strings.TrimPrefix(res.GetDisposition().String(), "PANEL_DISPOSITION_"))
	if len(res.GetObjectionsToTop()) > 0 {
		b.WriteString("\n# Objections to the top option\n\n")
		for _, o := range res.GetObjectionsToTop() {
			fmt.Fprintf(&b, "- \"%s\"\n", o.GetText())
		}
	}
	b.WriteString("\n# Dissents\n\n")
	if len(res.GetDissents()) == 0 {
		b.WriteString("None: every answer ranked the top option first.\n")
	}
	for i, d := range res.GetDissents() {
		fmt.Fprintf(&b, "## D%d: lens %s, ranked %s first\n\n", i+1,
			strings.ToLower(strings.TrimPrefix(d.GetLens().String(), "PANEL_LENS_")), d.GetRankedFirst())
		for _, o := range d.GetObjections() {
			fmt.Fprintf(&b, "- Objection to %s: \"%s\"\n", o.GetOptionId(), o.GetText())
		}
		if d.GetWouldChangeMind() != "" {
			fmt.Fprintf(&b, "- Would change mind: \"%s\"\n", d.GetWouldChangeMind())
		}
		b.WriteString("\n")
	}
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
