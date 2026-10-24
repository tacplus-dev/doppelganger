package daemon

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

//go:embed prompt.md
var systemPrompt string

// Answer is the engine's structured result. Kind and Provenance hold the enum
// suffixes the model is constrained to ("ANSWER", "QUOTED", ...).
type Answer struct {
	Kind            string   `json:"kind"`
	Answer          string   `json:"answer"`
	Reasoning       string   `json:"reasoning"`
	WouldChangeMind string   `json:"would_change_mind"`
	Provenance      string   `json:"provenance"`
	Sources         []string `json:"sources"`
	Missing         []string `json:"missing"`
}

const answerSchema = `{
  "type": "object",
  "properties": {
    "kind": {"type": "string", "enum": ["ANSWER", "NEEDS_CONTEXT", "BLOCKED_ON_HUMAN"]},
    "answer": {"type": "string"},
    "reasoning": {"type": "string"},
    "would_change_mind": {"type": "string"},
    "provenance": {"type": "string", "enum": ["QUOTED", "DERIVED"]},
    "sources": {"type": "array", "items": {"type": "string"}},
    "missing": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["kind", "answer", "reasoning", "would_change_mind", "provenance", "sources", "missing"]
}`

// Engine forms answers through a Provider with no tools. The caller's whole
// context (the tenant's and their own) goes into the prompt, and the model can
// read nothing else.
type Engine struct {
	Contexts *ContextStore
	Provider Provider
}

// maxAttempts bounds the model calls for one answer. An attempt is retried
// when the model cannot produce valid structured output, or when the answer puts
// words in Jody's mouth. If the last attempt still misquotes him, the quotation
// marks are stripped from the unmatched text (see stripQuotes), so the content
// survives but is no longer presented as his words.
const maxAttempts = 3

func (e *Engine) Answer(ctx context.Context, sess *Session, question string) (Answer, error) {
	repo, err := e.Contexts.Load(ctx, sess.Tenant, sess.Owner)
	if err != nil {
		return Answer{}, err
	}
	marked := markedQuotes(repo)
	req := Request{System: systemPrompt, Repo: repo, Turn: renderTurn(sess, question), Schema: answerSchema}
	var last error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		a, err := e.run(ctx, req)
		if errors.Is(err, errStructuredOutput) {
			last = err
			continue
		}
		if err != nil {
			return Answer{}, err
		}
		bad := unattributedQuotes(a, marked)
		if len(bad) == 0 {
			return a, nil
		}
		if attempt == maxAttempts {
			return stripQuotes(a, bad), nil
		}
		req.Turn += renderQuoteRejection(bad)
	}
	return Answer{}, fmt.Errorf("no acceptable answer after %d attempts: %w", maxAttempts, last)
}

// run makes one model call and decodes the structured answer.
func (e *Engine) run(ctx context.Context, req Request) (Answer, error) {
	raw, err := e.Provider.Complete(ctx, req)
	if err != nil {
		return Answer{}, err
	}
	var a Answer
	if err := json.Unmarshal(raw, &a); err != nil {
		return Answer{}, fmt.Errorf("decode answer: %w", err)
	}
	return a, nil
}

func renderQuoteRejection(bad []string) string {
	var b strings.Builder
	b.WriteString("\n# Your previous answer was rejected\n\n")
	b.WriteString("It put these in quotation marks, but they are not Jody's marked words (text inside *\"...\"* in the repo). ")
	b.WriteString("They are agent-written text from the repo, or your own paraphrase. Answer the same question again. ")
	b.WriteString("Use quotation marks only around Jody's marked words, copied exactly. ")
	b.WriteString("Text written by agents in the repo: drop the quotation marks and attribute it as \"the repo says\". ")
	b.WriteString("Text you are drafting for the caller (a bio, a sentence to use): give it without quotation marks, ")
	b.WriteString("for example on its own line after \"Suggested text:\".\n\n")
	for _, q := range bad {
		fmt.Fprintf(&b, "- %q\n", q)
	}
	return b.String()
}

// renderTurn is the user message: caller context, earlier turns, the question.
func renderTurn(sess *Session, question string) string {
	var b strings.Builder
	b.WriteString("# Caller context\n\n")
	if len(sess.Context) == 0 {
		b.WriteString("(none given)\n")
	}
	for _, c := range sess.Context {
		b.WriteString("- " + c + "\n")
	}
	if len(sess.Turns) > 0 {
		b.WriteString("\n# Earlier turns in this session\n")
		for i, t := range sess.Turns {
			ans, _ := json.Marshal(t.Answer)
			fmt.Fprintf(&b, "\n## Turn %d\n\nQuestion: %s\n\nYour answer: %s\n", i+1, t.Question, ans)
		}
	}
	b.WriteString("\n# Question\n\n" + question + "\n")
	return b.String()
}
