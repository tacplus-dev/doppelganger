// doppelganger answers questions on Jody's behalf from the ~/workspace/jody repo.
//
//	doppelganger daemon
//	doppelganger session create [--context TEXT]...
//	doppelganger session update SESSION_ID --context TEXT [--context TEXT]...
//	doppelganger ask SESSION_ID QUESTION
//
// Every client command prints JSON. The CLI is for agents we do not write
// (claude, codex); tools we write call the C2D API directly.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tacplus-dev/doppelganger/internal/daemon"
	c2dm "github.com/tacplus-dev/doppelganger/tacplus/c2dm"
	gc "github.com/tacplus-dev/doppelganger/tacplus/c2dm/grpc/clients/services/doppelganger"
	sm "github.com/tacplus-dev/doppelganger/tacplus/s2sm"
	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// answerTimeout covers a full claude run over the whole repo.
const answerTimeout = 5 * time.Minute

type contextFlags []string

func (c *contextFlags) String() string     { return fmt.Sprint(*c) }
func (c *contextFlags) Set(v string) error { *c = append(*c, v); return nil }

func main() {
	if err := run(os.Args[1:]); err != nil {
		printJSON(map[string]string{"error": err.Error()})
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: doppelganger daemon | session create|update | ask SESSION_ID QUESTION")
	}
	switch args[0] {
	case "daemon":
		return runDaemon(args[1:])
	case "session":
		if len(args) < 2 {
			return errors.New("usage: doppelganger session create|update")
		}
		switch args[1] {
		case "create":
			return sessionCreate(args[2:])
		case "update":
			return sessionUpdate(args[2:])
		}
		return fmt.Errorf("unknown session command %q", args[1])
	case "ask":
		return ask(args[1:])
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func runDaemon(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	repo := fs.String("repo", filepath.Join(home, "workspace", "jody"), "jody context repo")
	stateDir := fs.String("state-dir", filepath.Join(home, ".doppelganger", "sessions"), "session state directory")
	providerName := fs.String("provider", "anthropic", "model provider: anthropic | claude (the claude CLI, interim)")
	model := fs.String("model", "", "model for answers (default: the provider's default)")
	claudeBin := fs.String("claude", "claude", "claude CLI binary (provider=claude)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var provider daemon.Provider
	switch *providerName {
	case "anthropic":
		// Credentials: the SDK's default chain (federation env in the fleet,
		// the `ant auth login` profile on a workstation). Nothing is read here.
		provider = daemon.NewAnthropic(*model)
	case "claude":
		provider = &daemon.ClaudeCLI{Bin: *claudeBin, Model: *model}
	default:
		return fmt.Errorf("unknown provider %q", *providerName)
	}
	sessions, err := daemon.NewSessionStore(*stateDir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return sm.ServeC2DStandalone(ctx, sm.C2DSocketPath, &daemon.Server{
		Sessions: sessions,
		Engine:   &daemon.Engine{RepoDir: *repo, Provider: provider},
	})
}

func sessionCreate(args []string) error {
	fs := flag.NewFlagSet("session create", flag.ContinueOnError)
	var ctxs contextFlags
	fs.Var(&ctxs, "context", "context for the session (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return call(30*time.Second, func(ctx context.Context, c *gc.DoppelgangerClient) (proto.Message, error) {
		return c.CreateSession(ctx, &pb.CreateSessionRequest{Context: ctxs})
	})
}

func sessionUpdate(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: doppelganger session update SESSION_ID --context TEXT")
	}
	id := args[0]
	fs := flag.NewFlagSet("session update", flag.ContinueOnError)
	var ctxs contextFlags
	fs.Var(&ctxs, "context", "context to add (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if len(ctxs) == 0 {
		return errors.New("session update needs at least one --context")
	}
	return call(30*time.Second, func(ctx context.Context, c *gc.DoppelgangerClient) (proto.Message, error) {
		return c.UpdateSession(ctx, &pb.UpdateSessionRequest{SessionId: id, Context: ctxs})
	})
}

func ask(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: doppelganger ask SESSION_ID QUESTION")
	}
	return call(answerTimeout, func(ctx context.Context, c *gc.DoppelgangerClient) (proto.Message, error) {
		return c.GenerateAnswer(ctx, &pb.GenerateAnswerRequest{SessionId: args[0], Question: args[1]})
	})
}

// call dials the daemon through the tacplus C2D manager and prints the response.
func call(timeout time.Duration, fn func(context.Context, *gc.DoppelgangerClient) (proto.Message, error)) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	mgr, err := c2dm.Dial(ctx, sm.C2DSocketPath)
	if err != nil {
		return fmt.Errorf("daemon not reachable at %s: %w", sm.C2DSocketPath, err)
	}
	defer mgr.Close()
	c := mgr.Grpc().Clients().Services().DoppelgangerClient().WithTimeout(timeout)
	resp, err := fn(ctx, c)
	if err != nil {
		return err
	}
	out, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}.Marshal(resp)
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

func printJSON(v any) {
	out, _ := json.Marshal(v)
	fmt.Println(string(out))
}
