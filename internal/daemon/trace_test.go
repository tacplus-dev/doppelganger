// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"reflect"
	"testing"

	llmproxy_gc "github.com/tacplus-dev/doppelganger/tacplus/s2sm/grpc/clients/services/llmproxy"
	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	llmproxy_pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/llmproxy"
)

// Every handler copies its request's trace_id onto each downstream call the
// request causes: llmproxy and the record store's reads and writes
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
	sess, err := NewSessionStore(rec).Create(acmeCtx(), owner, acme, []string{"audience: Jody"})
	if err != nil {
		t.Fatal(err)
	}
	return sess.ID
}

func TestCreateSessionForwardsTraceID(t *testing.T) {
	rec := newFakeRecords()
	s := &Server{Sessions: NewSessionStore(rec)}
	if _, err := s.CreateSession(acmeCtx(), asUser("jody"), &pb.CreateSessionRequest{Tenant: acmeTid, TraceId: inboundTrace}); err != nil {
		t.Fatal(err)
	}
	wantTraced(t, rec.takeTraces(), map[string]int{"InsertIfAbsentSession": 1})
}

func TestUpdateSessionForwardsTraceID(t *testing.T) {
	rec := newFakeRecords()
	id := seedSession(t, rec, "jody")
	rec.takeTraces()
	s := &Server{Sessions: NewSessionStore(rec)}
	if _, err := s.UpdateSession(acmeCtx(), asUser("jody"), &pb.UpdateSessionRequest{Tenant: acmeTid, TraceId: inboundTrace, SessionId: id, Context: []string{"purpose: trace"}}); err != nil {
		t.Fatal(err)
	}
	wantTraced(t, rec.takeTraces(), map[string]int{"GetSession": 1, "ReplaceIfUnchangedSession": 1})
	if got := rec.sessions[id].GetTraceId(); got != inboundTrace {
		t.Fatalf("stored session trace_id = %q, want %q", got, inboundTrace)
	}
}

// A replacement reads the context's rows and writes each file and the
// retirement of each row it no longer carries in one Apply, under the
// request's trace_id.
func TestUpdateContextForwardsTraceID(t *testing.T) {
	rec := newFakeRecords()
	rec.seed(acme, scopeUser, "jody", "old.md", "retired by the replacement")
	rec.takeTraces()
	s := &Server{Engine: &Engine{Contexts: NewContextStore(rec)}}
	files := []*pb.ContextFile{{Path: "a.md", Content: []byte("a")}, {Path: "b.md", Content: []byte("b")}}
	if _, err := s.UpdateContext(acmeCtx(), asUser("jody"), &pb.UpdateContextRequest{Tenant: acmeTid, TraceId: inboundTrace, Files: files}); err != nil {
		t.Fatal(err)
	}
	wantTraced(t, rec.takeTraces(), map[string]int{"GetContextDocumentRows": 1, "Apply": 1})
}

func TestUpdateTenantContextForwardsTraceID(t *testing.T) {
	rec := newFakeRecords()
	rec.seed(acme, scopeTenant, acme, "old.md", "retired by the replacement")
	rec.takeTraces()
	s := &Server{Engine: &Engine{Contexts: NewContextStore(rec)}}
	files := []*pb.ContextFile{{Path: "policy.md", Content: []byte("p")}}
	if _, err := s.UpdateTenantContext(acmeCtx(), asUser("jody"), &pb.UpdateTenantContextRequest{Tenant: acmeTid, TraceId: inboundTrace, Files: files}); err != nil {
		t.Fatal(err)
	}
	wantTraced(t, rec.takeTraces(), map[string]int{"GetContextDocumentRows": 1, "Apply": 1})
}

// The answer goes through the real llmproxy provider: the reply the engine
// cannot decode is retried, and both llmproxy requests carry the trace_id,
// as do the session reads (the answer's and the turn's), the two context
// reads and the turn's write.
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
		Sessions: NewSessionStore(rec),
		Engine:   &Engine{Contexts: NewContextStore(rec), Provider: &LlmProxy{Client: client, ModelAlias: "m"}},
	}
	resp, err := s.GenerateAnswer(acmeCtx(), asUser("jody"), &pb.GenerateAnswerRequest{Tenant: acmeTid, TraceId: inboundTrace, SessionId: id, Question: "rewrite?"})
	if err != nil || resp.GetAnswer() != "fix now" {
		t.Fatalf("GenerateAnswer = %v, %v", resp, err)
	}
	calls := rec.takeTraces()
	for _, sent := range conn.sent {
		calls["GenerateCompletionAsService"] = append(calls["GenerateCompletionAsService"], sent.GetTraceId())
	}
	wantTraced(t, calls, map[string]int{"GetSession": 2, "GetContextDocumentRows": 2, "GenerateCompletionAsService": 2, "ReplaceIfUnchangedSession": 1})
}

// Every seat in both phases and under every lens, and the moderator in each
// of its three steps, are called under the request's trace_id, as are the
// principles' read and the run's insert.
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
	s := &Server{Panel: panel, Panels: NewPanelStore(rec)}
	resp, err := s.GeneratePanelAnswer(acmeCtx(), asUser("alice"), &pb.GeneratePanelAnswerRequest{Tenant: acmeTid,
		TraceId: inboundTrace, Question: grpcQuestion.Question, Situation: grpcQuestion.Situation, QuestionKind: "security-design",
	})
	if err != nil || len(resp.GetFailures()) != 0 || !resp.GetModeratorMerged() {
		t.Fatalf("GeneratePanelAnswer = %v, %v", resp, err)
	}
	calls := rec.takeTraces()
	want := map[string]int{"GetContextDocumentRows": 1, "InsertIfAbsentPanelRun": 1, "moderator": 3}
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
	runs := NewPanelStore(rec)
	run := &pb.PanelRun{Options: []*pb.PanelOption{{OptionId: "O1"}}}
	if err := runs.Create(withTraceID(acmeCtx(), "trace-of-the-ask"), "alice", acme, run); err != nil {
		t.Fatal(err)
	}
	rec.takeTraces()
	// Another instance reads the run from the store.
	s := &Server{Panels: NewPanelStore(rec)}
	if _, err := s.UpdatePanelDecision(acmeCtx(), asUser("jody"), &pb.UpdatePanelDecisionRequest{Tenant: acmeTid, TraceId: inboundTrace, PanelId: run.GetPanelId(), OptionId: "O1"}); err != nil {
		t.Fatal(err)
	}
	wantTraced(t, rec.takeTraces(), map[string]int{"GetPanelRun": 1, "ReplaceIfUnchangedPanelRun": 1})
	if got := rec.panelRuns[run.GetPanelId()].GetTraceId(); got != inboundTrace {
		t.Fatalf("stored run trace_id = %q, want the deciding request's %q", got, inboundTrace)
	}
}

func TestGetPanelTrackRecordForwardsTraceID(t *testing.T) {
	rec := newFakeRecords()
	s := &Server{Panels: NewPanelStore(rec)}
	if _, err := s.GetPanelTrackRecord(acmeCtx(), asUser("alice"), &pb.GetPanelTrackRecordRequest{Tenant: acmeTid, TraceId: inboundTrace}); err != nil {
		t.Fatal(err)
	}
	wantTraced(t, rec.takeTraces(), map[string]int{"GetPanelRunRows": 1})
}

// The llmproxy provider sets the trace_id its context carries on the request,
// for the engine's calls and for a panel seat's or the moderator's.
func TestLlmProxyForwardsTraceID(t *testing.T) {
	conn := &recordingConn{reply: &llmproxy_pb.GenerateCompletionResponse{Result: &llmproxy_pb.GenerateCompletionResponse_Text{Text: `{}`}}}
	p := &LlmProxy{Client: llmproxy_gc.NewLlmProxyClient(conn, func() (string, error) { return "token", nil }), ModelAlias: "m"}
	ctx := withTraceID(acmeCtx(), inboundTrace)
	if _, err := p.Complete(ctx, Request{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.CompleteWithModel(ctx, Request{}); err != nil {
		t.Fatal(err)
	}
	wantTraced(t, map[string][]string{"GenerateCompletionAsService": {conn.sent[0].GetTraceId(), conn.sent[1].GetTraceId()}},
		map[string]int{"GenerateCompletionAsService": 2})
}
