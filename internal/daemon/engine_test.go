package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// scriptedProvider returns its replies in order and records every request.
type scriptedProvider struct {
	replies []any // json.RawMessage or error
	reqs    []Request
}

func (p *scriptedProvider) Complete(_ context.Context, req Request) (json.RawMessage, error) {
	p.reqs = append(p.reqs, req)
	if len(p.replies) == 0 {
		return nil, errStructuredOutput
	}
	r := p.replies[0]
	p.replies = p.replies[1:]
	if err, ok := r.(error); ok {
		return nil, err
	}
	return r.(json.RawMessage), nil
}

func answerJSON(answer string) json.RawMessage {
	b, _ := json.Marshal(Answer{Kind: "ANSWER", Answer: answer, Provenance: "DERIVED", Sources: []string{}, Missing: []string{}})
	return b
}

func newEngine(t *testing.T, p Provider) *Engine {
	t.Helper()
	rec := newFakeRecords()
	rec.seed(acme, scopeTenant, acme, "notes.md", `Jody said *"I really want stuff that matters."* and the repo recommends recommendations.`)
	return &Engine{Contexts: NewContextStore(rec, rec.publish), Provider: p}
}

func TestEngineRequestShape(t *testing.T) {
	p := &scriptedProvider{replies: []any{answerJSON("ok")}}
	e := newEngine(t, p)
	sess := &Session{Tenant: acme, Context: []string{"audience: Jody"}}
	if _, err := e.Answer(context.Background(), sess, "what matters?"); err != nil {
		t.Fatal(err)
	}
	req := p.reqs[0]
	if req.System != systemPrompt || req.Schema != answerSchema {
		t.Fatal("system prompt or schema not passed through")
	}
	if !strings.Contains(req.Repo, "stuff that matters") || !strings.Contains(req.Turn, "audience: Jody") || !strings.Contains(req.Turn, "what matters?") {
		t.Fatalf("request = %+v", req)
	}
}

func TestEngineRetriesStructuredOutputFailure(t *testing.T) {
	p := &scriptedProvider{replies: []any{errStructuredOutput, answerJSON("second time")}}
	e := newEngine(t, p)
	a, err := e.Answer(context.Background(), &Session{Tenant: acme}, "q")
	if err != nil || a.Answer != "second time" || len(p.reqs) != 2 {
		t.Fatalf("a=%+v err=%v calls=%d", a, err, len(p.reqs))
	}
}

func TestEngineRejectsUnmarkedQuoteThenAccepts(t *testing.T) {
	p := &scriptedProvider{replies: []any{
		answerJSON(`Jody said "the repo recommends recommendations" so recommend.`),
		answerJSON(`Jody said "I really want stuff that matters" so recommend.`),
	}}
	e := newEngine(t, p)
	a, err := e.Answer(context.Background(), &Session{Tenant: acme}, "q")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.Answer, `"I really want stuff that matters"`) {
		t.Fatalf("answer = %q", a.Answer)
	}
	if len(p.reqs) != 2 || !strings.Contains(p.reqs[1].Turn, "previous answer was rejected") || !strings.Contains(p.reqs[1].Turn, "the repo recommends recommendations") {
		t.Fatalf("second request did not carry the rejection: %+v", p.reqs)
	}
}

func TestEngineStripsQuotesOnLastAttempt(t *testing.T) {
	bad := answerJSON(`He said "we must ship it tomorrow morning" to me.`)
	p := &scriptedProvider{replies: []any{bad, bad, bad}}
	e := newEngine(t, p)
	a, err := e.Answer(context.Background(), &Session{Tenant: acme}, "q")
	if err != nil {
		t.Fatal(err)
	}
	if a.Answer != `He said we must ship it tomorrow morning to me.` || len(p.reqs) != maxAttempts {
		t.Fatalf("answer = %q calls=%d", a.Answer, len(p.reqs))
	}
}

func TestEngineGivesUpAfterMaxStructuredOutputFailures(t *testing.T) {
	p := &scriptedProvider{}
	e := newEngine(t, p)
	if _, err := e.Answer(context.Background(), &Session{Tenant: acme}, "q"); err == nil || len(p.reqs) != maxAttempts {
		t.Fatalf("err=%v calls=%d", err, len(p.reqs))
	}
}
