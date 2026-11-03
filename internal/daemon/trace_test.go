// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"reflect"
	"testing"

	llmproxy_gc "github.com/tacplus-dev/doppelganger/tacplus/s2sm/grpc/clients/services/llmproxy"
	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	llmproxy_pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/llmproxy"
)

// Every handler copies its request's trace_id onto each downstream call the
// request causes: llmproxy, the record query plane and the record publishes
// (tacplus USER.md, Distributed Tracing; doppelganger#12). Each test below
// sends one request with a known trace_id through the handler and reads the
// trace_id every fake downstream received.

const inboundTrace = "trace-inbound-0001"

// wantTraced fails unless calls (call name -> the trace_id of each call) are
// exactly want (call name -> how many calls), each carrying inboundTrace.
func wantTraced(t *testing.T, calls map[string][]string, want map[string]int) {
	t.Helper()
	got := map[string]int{}
	for name, ids := range calls {
		got[name] = len(ids)
		for _, id := range ids {
			if id != inboundTrace {
				t.Errorf("%s carried trace_id %q, want %q", name, id, inboundTrace)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("downstream calls = %v, want %v", got, want)
	}
}

// seedSession is a session row already in the store, as another instance (or
// this one before a restart) left it: reading it is a query on the plane.
func seedSession(t *testing.T, rec *fakeRecords, owner string) string {
	t.Helper()
	sess, err := NewSessionStore(rec, rec.publishSession).Create(context.Background(), owner, acme, []string{"audience: Jody"})
	if err != nil {
		t.Fatal(err)
	}
	return sess.ID
}

func TestCreateSessionForwardsTraceID(t *testing.T) {
	rec := newFakeRecords()
	s := &Server{Sessions: NewSessionStore(rec, rec.publishSession)}
	if _, err := s.CreateSession(context.Background(), asUser("jody"), &pb.CreateSessionRequest{TraceId: inboundTrace}); err != nil {
		t.Fatal(err)
	}
	wantTraced(t, rec.takeTraces(), map[string]int{"publishSession": 1})
}

func TestUpdateSessionForwardsTraceID(t *testing.T) {
	rec := newFakeRecords()
	id := seedSession(t, rec, "jody")
	rec.takeTraces()
	s := &Server{Sessions: NewSessionStore(rec, rec.publishSession)}
	if _, err := s.UpdateSession(context.Background(), asUser("jody"), &pb.UpdateSessionRequest{TraceId: inboundTrace, SessionId: id, Context: []string{"purpose: trace"}}); err != nil {
		t.Fatal(err)
	}
	wantTraced(t, rec.takeTraces(), map[string]int{"GetSessionRows": 1, "publishSession": 1})
	if got := rec.sessions[id].GetTraceId(); got != inboundTrace {
		t.Fatalf("stored session trace_id = %q, want %q", got, inboundTrace)
	}
}

// A replacement reads the context's rows, publishes each file and retires
// each row it no longer carries, all under the request's trace_id.
func TestUpdateContextForwardsTraceID(t *testing.T) {
	rec := newFakeRecords()
	rec.seed(acme, scopeUser, "jody", "old.md", "retired by the replacement")
	rec.takeTraces()
	s := &Server{Engine: &Engine{Contexts: NewContextStore(rec, rec.publish)}}
	files := []*pb.ContextFile{{Path: "a.md", Content: []byte("a")}, {Path: "b.md", Content: []byte("b")}}
	if _, err := s.UpdateContext(context.Background(), asUser("jody"), &pb.UpdateContextRequest{TraceId: inboundTrace, Files: files}); err != nil {
		t.Fatal(err)
	}
	wantTraced(t, rec.takeTraces(), map[string]int{"GetContextDocumentRows": 1, "publish": 3})
}

func TestUpdateTenantContextForwardsTraceID(t *testing.T) {
	rec := newFakeRecords()
	rec.seed(acme, scopeTenant, acme, "old.md", "retired by the replacement")
	rec.takeTraces()
	s := &Server{Engine: &Engine{Contexts: NewContextStore(rec, rec.publish)}}
	files := []*pb.ContextFile{{Path: "policy.md", Content: []byte("p")}}
	if _, err := s.UpdateTenantContext(context.Background(), asUser("jody"), &pb.UpdateTenantContextRequest{TraceId: inboundTrace, Files: files}); err != nil {
		t.Fatal(err)
	}
	wantTraced(t, rec.takeTraces(), map[string]int{"GetContextDocumentRows": 1, "publish": 2})
}

// The answer goes through the real llmproxy provider: the reply the engine
// cannot decode is retried, and both llmproxy requests carry the trace_id,
// as do the session reads (the answer's and the turn's), the two context
// reads and the turn's publish.
func TestGenerateAnswerForwardsTraceID(t *testing.T) {
	rec := newFakeRecords()
	rec.seed(acme, scopeTenant, acme, "tenant.md", "tenant context")
	rec.seed(acme, scopeUser, "jody", "me.md", "Jody wants recommendations.")
	id := seedSession(t, rec, "jody")
	rec.takeTraces()
	conn := &recordingConn{
		replies: []*llmproxy_pb.GenerateCompletionResponse{{Result: &llmproxy_pb.GenerateCompletionResponse_Text{Text: "Sure! Here it is, not as JSON."}}},
		reply:   &llmproxy_pb.GenerateCompletionResponse{Result: &llmproxy_pb.GenerateCompletionResponse_Text{Text: string(answerJSON("fix now"))}},
	}
	client := llmproxy_gc.NewLlmProxyClient(conn, func() (string, error) { return "token", nil })
	s := &Server{
		Sessions: NewSessionStore(rec, rec.publishSession),
		Engine:   &Engine{Contexts: NewContextStore(rec, rec.publish), Provider: &LlmProxy{Client: client, ModelAlias: "m"}},
	}
	resp, err := s.GenerateAnswer(context.Background(), asUser("jody"), &pb.GenerateAnswerRequest{TraceId: inboundTrace, SessionId: id, Question: "rewrite?"})
	if err != nil || resp.GetAnswer() != "fix now" {
		t.Fatalf("GenerateAnswer = %v, %v", resp, err)
	}
	calls := rec.takeTraces()
	for _, sent := range conn.sent {
		calls["GenerateCompletionAsService"] = append(calls["GenerateCompletionAsService"], sent.GetTraceId())
	}
	wantTraced(t, calls, map[string]int{"GetSessionRows": 2, "GetContextDocumentRows": 2, "GenerateCompletionAsService": 2, "publishSession": 1})
}

// Every seat in both phases and under every lens, and the moderator in each
// of its three steps, are called under the request's trace_id, as are the
// principles' read and the run's publish.
func TestGeneratePanelAnswerForwardsTraceID(t *testing.T) {
	mod := &fakeModerator{model: "claude-opus-5-5", frame: jsonReply(grpcFraming), merge: mergeAll("Build it on gRPC", "TCP"), summary: quoteEverything}
	seats := []*fakePanelist{
		{model: "claude-opus-5-5", phase1: proposals("Build it on gRPC", "Custom TCP framing"), phase2: ranking(80, "", "Build it on gRPC")},
		{model: "gpt-5.2", phase1: proposals("Build it on gRPC"), phase2: ranking(70, "", "Build it on gRPC")},
		{model: "gemini-3-pro", phase1: proposals("Raw TCP framing"), phase2: ranking(60, "", "TCP")},
	}
	panel, rec := newPanel(t, seats...)
	panel.Moderator = &PanelSeat{Alias: "claude-opus-latest", Panelist: mod}
	rec.takeTraces()
	s := &Server{Panel: panel, Panels: NewPanelStore(rec, rec.publishPanelRun)}
	resp, err := s.GeneratePanelAnswer(context.Background(), asUser("alice"), &pb.GeneratePanelAnswerRequest{
		TraceId: inboundTrace, Question: grpcQuestion.Question, Situation: grpcQuestion.Situation, QuestionKind: "security-design",
	})
	if err != nil || len(resp.GetFailures()) != 0 || !resp.GetModeratorMerged() {
		t.Fatalf("GeneratePanelAnswer = %v, %v", resp, err)
	}
	calls := rec.takeTraces()
	want := map[string]int{"GetContextDocumentRows": 1, "publishPanelRun": 1, "moderator": 3}
	calls["moderator"] = mod.traces
	for i, seat := range seats {
		name := panel.Seats[i].Alias
		calls[name] = seat.traces
		want[name] = 1 + len(panelLenses)
	}
	wantTraced(t, calls, want)
	if got := rec.panelRuns[resp.GetPanelId()].GetTraceId(); got != inboundTrace {
		t.Fatalf("stored run trace_id = %q, want %q", got, inboundTrace)
	}
}

// A decision rewrites the run under the deciding request's trace_id, not the
// one the run was asked under.
func TestUpdatePanelDecisionForwardsTraceID(t *testing.T) {
	rec := newFakeRecords()
	runs := NewPanelStore(rec, rec.publishPanelRun)
	run := &pb.PanelRun{Options: []*pb.PanelOption{{OptionId: "O1"}}}
	if err := runs.Create(withTraceID(context.Background(), "trace-of-the-ask"), "alice", acme, run); err != nil {
		t.Fatal(err)
	}
	rec.takeTraces()
	// Another instance, with nothing cached, reads the run from the store.
	s := &Server{Panels: NewPanelStore(rec, rec.publishPanelRun)}
	if _, err := s.UpdatePanelDecision(context.Background(), asUser("jody"), &pb.UpdatePanelDecisionRequest{TraceId: inboundTrace, PanelId: run.GetPanelId(), OptionId: "O1"}); err != nil {
		t.Fatal(err)
	}
	wantTraced(t, rec.takeTraces(), map[string]int{"GetPanelRunRows": 1, "publishPanelRun": 1})
	if got := rec.panelRuns[run.GetPanelId()].GetTraceId(); got != inboundTrace {
		t.Fatalf("stored run trace_id = %q, want the deciding request's %q", got, inboundTrace)
	}
}

func TestGetPanelTrackRecordForwardsTraceID(t *testing.T) {
	rec := newFakeRecords()
	s := &Server{Panels: NewPanelStore(rec, rec.publishPanelRun)}
	if _, err := s.GetPanelTrackRecord(context.Background(), asUser("alice"), &pb.GetPanelTrackRecordRequest{TraceId: inboundTrace}); err != nil {
		t.Fatal(err)
	}
	wantTraced(t, rec.takeTraces(), map[string]int{"GetPanelRunRows": 1})
}

// The llmproxy provider sets the trace_id its context carries on the request,
// for the engine's calls and for a panel seat's or the moderator's.
func TestLlmProxyForwardsTraceID(t *testing.T) {
	conn := &recordingConn{reply: &llmproxy_pb.GenerateCompletionResponse{Result: &llmproxy_pb.GenerateCompletionResponse_Text{Text: `{}`}}}
	p := &LlmProxy{Client: llmproxy_gc.NewLlmProxyClient(conn, func() (string, error) { return "token", nil }), ModelAlias: "m"}
	ctx := withTraceID(context.Background(), inboundTrace)
	if _, err := p.Complete(ctx, Request{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.CompleteWithModel(ctx, Request{}); err != nil {
		t.Fatal(err)
	}
	wantTraced(t, map[string][]string{"GenerateCompletionAsService": {conn.sent[0].GetTraceId(), conn.sent[1].GetTraceId()}},
		map[string]int{"GenerateCompletionAsService": 2})
}
