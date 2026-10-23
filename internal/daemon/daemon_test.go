package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c2dm "github.com/tacplus-dev/doppelganger/tacplus/c2dm"
	sm "github.com/tacplus-dev/doppelganger/tacplus/s2sm"
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

// TestEndToEnd drives the real C2D socket through the tacplus-generated
// standalone host and client, with a fake claude binary standing in for the
// model. It binds its own socket, not sm.C2DSocketPath, so it never takes over
// the socket of a daemon already running on this machine.
func TestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	write(t, filepath.Join(repo, "README.md"), "Jody wants recommendations.")
	// The fake records its stdin so the test can check what the model was given.
	fake := filepath.Join(dir, "claude")
	write(t, fake, `#!/bin/sh
cat > "`+dir+`/stdin.txt"
echo '{"is_error":false,"result":"","structured_output":{"kind":"ANSWER","answer":"fix now, note the rewrite","reasoning":"r","would_change_mind":"w","provenance":"DERIVED","sources":["README.md"],"missing":[]}}'
`)
	if err := os.Chmod(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := NewSessionStore(filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	// Unix socket paths are length-limited; t.TempDir() can be too long on macOS.
	sock, err := os.MkdirTemp("/tmp", "dg")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sock)
	socketPath := filepath.Join(sock, "s.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- sm.ServeC2DStandalone(ctx, socketPath, &Server{Sessions: store, Engine: &Engine{RepoDir: repo, Provider: &ClaudeCLI{Bin: fake}}})
	}()

	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	defer dialCancel()
	var mgr *c2dm.ServiceManager
	for {
		if mgr, err = c2dm.Dial(dialCtx, socketPath); err == nil {
			break
		}
		if dialCtx.Err() != nil {
			t.Fatalf("dial: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer mgr.Close()
	c := mgr.Grpc().Clients().Services().DoppelgangerClient()

	created, err := c.CreateSession(ctx, &pb.CreateSessionRequest{Context: []string{"audience: Jody"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateSession(ctx, &pb.UpdateSessionRequest{SessionId: created.GetSessionId(), Context: []string{"purpose: e2e"}}); err != nil {
		t.Fatal(err)
	}
	resp, err := c.GenerateAnswer(ctx, &pb.GenerateAnswerRequest{SessionId: created.GetSessionId(), Question: "rewrite?"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetKind() != pb.AnswerKind_ANSWER_KIND_ANSWER || resp.GetProvenance() != pb.Provenance_PROVENANCE_DERIVED {
		t.Fatalf("enum mapping wrong: %v / %v", resp.GetKind(), resp.GetProvenance())
	}
	if resp.GetAnswer() != "fix now, note the rewrite" || resp.GetSources()[0] != "README.md" {
		t.Fatalf("resp = %v", resp)
	}
	stdin, err := os.ReadFile(filepath.Join(dir, "stdin.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Jody wants recommendations.", "audience: Jody", "purpose: e2e", "rewrite?"} {
		if !strings.Contains(string(stdin), want) {
			t.Errorf("model input missing %q", want)
		}
	}
	sess, err := store.Get(created.GetSessionId())
	if err != nil || len(sess.Turns) != 1 {
		t.Fatalf("turn not persisted: %v %v", sess, err)
	}

	_, err = c.GenerateAnswer(ctx, &pb.GenerateAnswerRequest{SessionId: "0123456789abcdef0123456789abcdef", Question: "q"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("unknown session err = %v, want NotFound", err)
	}

	// The generated wrapper validates on the server too: a request the client
	// would refuse to send (session_id is assert_required), sent on the raw stub,
	// never reaches the handler.
	_, err = pb.NewDoppelgangerClient(mgr.Conn()).GenerateAnswer(ctx, &pb.GenerateAnswerRequest{Question: "q"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("server-side validation err = %v, want InvalidArgument", err)
	}

	cancel()
	if err := <-serveErr; err != nil {
		t.Fatalf("ServeC2DStandalone returned %v on shutdown", err)
	}
	if _, err := os.Stat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still present after shutdown: %v", err)
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
