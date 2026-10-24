package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/doppelganger/tacplus/chassis/servicemanager"
	"github.com/tacplus-dev/doppelganger/tacplus/chassis/token"
	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	token_pb "github.com/tacplus-dev/doppelganger/tacplus/types/sys/token"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// asUser is a verified caller whose token names principal.
func asUser(principal string) *servicemanager.Caller {
	tok := token.NewImmutableToken(&token_pb.Token{Payload: map[string]string{}}).SetPrincipal(principal)
	return servicemanager.NewCaller(tok, "")
}

func TestSessionStoreRoundTrip(t *testing.T) {
	store, err := NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create("u1", []string{"audience: Jody"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendContext(sess.ID, "u1", []string{"purpose: test"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTurn(sess.ID, "u1", Turn{Question: "q1", Answer: Answer{Kind: "ANSWER"}}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(sess.ID, "u1")
	if err != nil {
		t.Fatal(err)
	}
	// Another user's session does not exist as far as they can tell.
	if _, err := store.Get(sess.ID, "u2"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get as another user err = %v, want ErrSessionNotFound", err)
	}
	if err := store.AppendContext(sess.ID, "u2", []string{"x"}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("AppendContext as another user err = %v, want ErrSessionNotFound", err)
	}
	if len(got.Context) != 2 || got.Context[1] != "purpose: test" {
		t.Fatalf("context = %v", got.Context)
	}
	if len(got.Turns) != 1 || got.Turns[0].Question != "q1" {
		t.Fatalf("turns = %v", got.Turns)
	}
}

func TestSessionStoreRejectsUnknownAndMalformedIDs(t *testing.T) {
	store, err := NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"0123456789abcdef0123456789abcdef", "../../etc/passwd", ""} {
		if _, err := store.Get(id, "u1"); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("Get(%q) err = %v, want ErrSessionNotFound", id, err)
		}
	}
}

func TestRenderTurnReplaysEarlierTurns(t *testing.T) {
	sess := &Session{
		Context: []string{"audience: an agent working for Jody"},
		Turns:   []Turn{{Question: "rewrite or fix?", Answer: Answer{Kind: "ANSWER", Answer: "rewrite"}}},
	}
	msg := renderTurn(sess, "a rewrite takes two weeks; fix now and note the rewrite?")
	for _, want := range []string{"audience: an agent working for Jody", "rewrite or fix?", `"answer":"rewrite"`, "two weeks"} {
		if !strings.Contains(msg, want) {
			t.Errorf("rendered turn missing %q", want)
		}
	}
}

func TestLoadRepoExcludesInboxAndGit(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "README.md"), "kept")
	write(t, filepath.Join(dir, "legal", "case.md"), "kept too")
	write(t, filepath.Join(dir, "inbox", "draft.md"), "UNREVIEWED")
	write(t, filepath.Join(dir, ".git", "x.md"), "GITINTERNAL")
	got, n, err := loadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || !strings.Contains(got, "kept too") || strings.Contains(got, "UNREVIEWED") || strings.Contains(got, "GITINTERNAL") {
		t.Fatalf("loadRepo = %d %q", n, got)
	}
	if _, n, err := loadRepo(filepath.Join(dir, "absent")); err != nil || n != 0 {
		t.Fatalf("a missing context dir must read as empty, got n=%d err=%v", n, err)
	}
}

// The tenant's context and a user's own are replaced whole, land only inside
// the store, and are read together with their scope; a user sees no other
// user's files.
func TestContextStoreReplaceAndLoad(t *testing.T) {
	c := NewContextStore(t.TempDir())
	if _, _, err := c.Replace(c.TenantDir(), []*pb.ContextFile{{Path: "../escape.md", Content: []byte("x")}}); !errors.Is(err, errBadContextPath) {
		t.Fatalf("parent path err = %v, want errBadContextPath", err)
	}
	for _, bad := range []string{"/abs.md", "a/../../b.md", "", "a//b.md", "./x.md"} {
		if _, _, err := c.Replace(c.TenantDir(), []*pb.ContextFile{{Path: bad}}); err == nil {
			t.Errorf("path %q accepted", bad)
		}
	}
	n, bytes, err := c.Replace(c.TenantDir(), []*pb.ContextFile{{Path: "policy/tone.md", Content: []byte("tenant tone")}, {Path: "logo.png", Content: []byte{1, 2}}})
	if err != nil || n != 2 || bytes != 13 {
		t.Fatalf("Replace = %d %d %v", n, bytes, err)
	}
	if _, _, err := c.Replace(c.UserDir("alice@x"), []*pb.ContextFile{{Path: "me.md", Content: []byte("alice likes brevity")}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Replace(c.UserDir("bob"), []*pb.ContextFile{{Path: "me.md", Content: []byte("BOB SECRET")}}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Load("alice@x")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## tenant context", `path="policy/tone.md"`, "tenant tone", "## user context", "alice likes brevity"} {
		if !strings.Contains(got, want) {
			t.Errorf("Load missing %q", want)
		}
	}
	if strings.Contains(got, "BOB SECRET") || strings.Contains(got, "logo.png") {
		t.Fatalf("Load leaked another user's file or a non-markdown file: %q", got)
	}
	// A replacement is whole: the earlier file is gone, not merged.
	if _, _, err := c.Replace(c.TenantDir(), []*pb.ContextFile{{Path: "new.md", Content: []byte("replaced")}}); err != nil {
		t.Fatal(err)
	}
	got, _ = c.Load("alice@x")
	if strings.Contains(got, "tenant tone") || !strings.Contains(got, "replaced") {
		t.Fatalf("Replace must replace whole: %q", got)
	}
	// Nothing anywhere is a refusal, not an empty prompt.
	if _, err := NewContextStore(t.TempDir()).Load("nobody"); err == nil {
		t.Fatal("Load with no context must fail")
	}
}

// TestServerRoundTrip drives the handler the generated wrapper serves:
// sessions are created, extended and answered; the model sees the repo, the
// context and the question; turns persist; an unknown session is NotFound.
// Request validation and auth are the generated wrapper's and are tested in
// tacplus, not here.
func TestServerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSessionStore(filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	p := &scriptedProvider{replies: []any{json.RawMessage(`{"kind":"ANSWER","answer":"fix now, note the rewrite","reasoning":"r","would_change_mind":"w","provenance":"DERIVED","sources":["README.md"],"missing":[]}`)}}
	s := &Server{Sessions: store, Engine: &Engine{Contexts: NewContextStore(filepath.Join(dir, "repo")), Provider: p}}
	ctx := context.Background()
	jody := asUser("jody")

	// No caller is a refusal on every RPC.
	if _, err := s.CreateSession(ctx, nil, &pb.CreateSessionRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no caller err = %v, want Unauthenticated", err)
	}
	// The user's own context arrives through the service, not a volume.
	if _, err := s.UpdateContext(ctx, jody, &pb.UpdateContextRequest{Files: []*pb.ContextFile{{Path: "README.md", Content: []byte("Jody wants recommendations.")}}}); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateSession(ctx, jody, &pb.CreateSessionRequest{Context: []string{"audience: Jody"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateSession(ctx, jody, &pb.UpdateSessionRequest{SessionId: created.GetSessionId(), Context: []string{"purpose: e2e"}}); err != nil {
		t.Fatal(err)
	}
	// Another user of the tenant cannot touch Jody's session.
	if _, err := s.GenerateAnswer(ctx, asUser("someone-else"), &pb.GenerateAnswerRequest{SessionId: created.GetSessionId(), Question: "q"}); status.Code(err) != codes.NotFound {
		t.Fatalf("another user's GenerateAnswer err = %v, want NotFound", err)
	}
	resp, err := s.GenerateAnswer(ctx, jody, &pb.GenerateAnswerRequest{SessionId: created.GetSessionId(), Question: "rewrite?"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetKind() != pb.AnswerKind_ANSWER_KIND_ANSWER || resp.GetProvenance() != pb.Provenance_PROVENANCE_DERIVED {
		t.Fatalf("enum mapping wrong: %v / %v", resp.GetKind(), resp.GetProvenance())
	}
	if resp.GetAnswer() != "fix now, note the rewrite" || resp.GetSources()[0] != "README.md" {
		t.Fatalf("resp = %v", resp)
	}
	seen := p.reqs[0].Repo + p.reqs[0].Turn
	for _, want := range []string{"Jody wants recommendations.", "audience: Jody", "purpose: e2e", "rewrite?"} {
		if !strings.Contains(seen, want) {
			t.Errorf("model input missing %q", want)
		}
	}
	sess, err := store.Get(created.GetSessionId(), "jody")
	if err != nil || len(sess.Turns) != 1 {
		t.Fatalf("turn not persisted: %v %v", sess, err)
	}
	_, err = s.GenerateAnswer(ctx, jody, &pb.GenerateAnswerRequest{SessionId: "0123456789abcdef0123456789abcdef", Question: "q"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("unknown session err = %v, want NotFound", err)
	}
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
