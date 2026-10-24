package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSessionStoreRoundTrip(t *testing.T) {
	store, err := NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create([]string{"audience: Jody"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendContext(sess.ID, []string{"purpose: test"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTurn(sess.ID, Turn{Question: "q1", Answer: Answer{Kind: "ANSWER"}}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
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
		if _, err := store.Get(id); !errors.Is(err, ErrSessionNotFound) {
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
	got, err := loadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "kept too") || strings.Contains(got, "UNREVIEWED") || strings.Contains(got, "GITINTERNAL") {
		t.Fatalf("loadRepo = %q", got)
	}
}

// TestServerRoundTrip drives the handler the generated wrapper serves:
// sessions are created, extended and answered; the model sees the repo, the
// context and the question; turns persist; an unknown session is NotFound.
// Request validation and auth are the generated wrapper's and are tested in
// tacplus, not here.
func TestServerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	write(t, filepath.Join(repo, "README.md"), "Jody wants recommendations.")
	store, err := NewSessionStore(filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	p := &scriptedProvider{replies: []any{json.RawMessage(`{"kind":"ANSWER","answer":"fix now, note the rewrite","reasoning":"r","would_change_mind":"w","provenance":"DERIVED","sources":["README.md"],"missing":[]}`)}}
	s := &Server{Sessions: store, Engine: &Engine{RepoDir: repo, Provider: p}}
	ctx := context.Background()

	created, err := s.CreateSession(ctx, nil, &pb.CreateSessionRequest{Context: []string{"audience: Jody"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateSession(ctx, nil, &pb.UpdateSessionRequest{SessionId: created.GetSessionId(), Context: []string{"purpose: e2e"}}); err != nil {
		t.Fatal(err)
	}
	resp, err := s.GenerateAnswer(ctx, nil, &pb.GenerateAnswerRequest{SessionId: created.GetSessionId(), Question: "rewrite?"})
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
	sess, err := store.Get(created.GetSessionId())
	if err != nil || len(sess.Turns) != 1 {
		t.Fatalf("turn not persisted: %v %v", sess, err)
	}
	_, err = s.GenerateAnswer(ctx, nil, &pb.GenerateAnswerRequest{SessionId: "0123456789abcdef0123456789abcdef", Question: "q"})
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
