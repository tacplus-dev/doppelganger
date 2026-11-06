// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	query_pb "github.com/tacplus-dev/doppelganger/tacplus/types/vservices/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Stratum ingests no record from the event bus (stratum#2): every record is
// written through its write surface (doppelganger#7). Each record type below
// is written through a handler, read back, delivered by Watch<T>Changes, and
// refused, nothing written, while the redo is unreachable.

// TestNoRecordIsPublishedOnTheBus: the proto declares no event_publish, so
// tacplus generates no publisher for a record's topic, and no code reaches
// for one.
func TestNoRecordIsPublishedOnTheBus(t *testing.T) {
	root := filepath.Join("..", "..")
	src, err := os.ReadFile(filepath.Join(root, "proto", "services", "doppelganger.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "spec.event_publish") {
		t.Error("doppelganger.proto declares an event_publish: stratum would never store what it publishes")
	}
	if strings.Contains(string(src), `s2s_subscribe: {auth_services: ["stratum"]}`) {
		t.Error("a record still grants stratum its topic: stratum subscribes to none")
	}
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), "BusStreamPublisher") {
				t.Errorf("%s publishes a record on the bus", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// watchedSessions (and its two siblings) is every item Watch<T>Changes
// delivers from the start, in order.
func watchedSessions(t *testing.T, rec *fakeRecords) []*pb.Session {
	t.Helper()
	events, err := rec.WatchSessionChanges(acmeCtx(), &query_pb.WatchSessionChangesRequest{TargetTenant: acmeTid, TenantNamespace: acme})
	if err != nil {
		t.Fatal(err)
	}
	var out []*pb.Session
	for e, err := range events {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e.GetDelta().GetItem())
	}
	return out
}

func watchedPanelRuns(t *testing.T, rec *fakeRecords) []*pb.PanelRun {
	t.Helper()
	events, err := rec.WatchPanelRunChanges(acmeCtx(), &query_pb.WatchPanelRunChangesRequest{TargetTenant: acmeTid, TenantNamespace: acme})
	if err != nil {
		t.Fatal(err)
	}
	var out []*pb.PanelRun
	for e, err := range events {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e.GetDelta().GetItem())
	}
	return out
}

func watchedContextDocuments(t *testing.T, rec *fakeRecords) []*pb.ContextDocument {
	t.Helper()
	events, err := rec.WatchContextDocumentChanges(acmeCtx(), &query_pb.WatchContextDocumentChangesRequest{TargetTenant: acmeTid, TenantNamespace: acme})
	if err != nil {
		t.Fatal(err)
	}
	var out []*pb.ContextDocument
	for e, err := range events {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e.GetDelta().GetItem())
	}
	return out
}

func TestSessionThroughWriteSurface(t *testing.T) {
	rec := newFakeRecords()
	s := &Server{Sessions: NewSessionStore(rec)}
	ctx, jody := acmeCtx(), asUser("jody")

	// Write: inserted in acme's stratum.
	created, err := s.CreateSession(ctx, jody, &pb.CreateSessionRequest{Tenant: acmeTid, Context: []string{"audience: Jody"}})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetSessionId()
	// Update: rewritten on the revision read.
	if _, err := s.UpdateSession(ctx, jody, &pb.UpdateSessionRequest{Tenant: acmeTid, SessionId: id, Context: []string{"purpose: test"}}); err != nil {
		t.Fatal(err)
	}
	// Read: Get on the key, by another instance with nothing of its own.
	sess, err := NewSessionStore(rec).Get(ctx, id, "jody")
	if err != nil || len(sess.Context) != 2 || sess.Context[1] != "purpose: test" || sess.Tenant != acme {
		t.Fatalf("session read back = %+v, %v", sess, err)
	}
	// Watch: the insert, then the update.
	w := watchedSessions(t, rec)
	if len(w) != 2 || w[0].GetSessionId() != id || len(w[0].GetContext()) != 1 || len(w[1].GetContext()) != 2 {
		t.Fatalf("watched = %v", w)
	}

	// Redo unreachable: neither an insert nor an update is written, and the
	// caller is told to try again.
	rec.unreachable = true
	if _, err := s.CreateSession(ctx, jody, &pb.CreateSessionRequest{Tenant: acmeTid}); status.Code(err) != codes.Unavailable {
		t.Fatalf("CreateSession, redo unreachable: err = %v, want Unavailable", err)
	}
	if _, err := s.UpdateSession(ctx, jody, &pb.UpdateSessionRequest{Tenant: acmeTid, SessionId: id, Context: []string{"lost"}}); status.Code(err) != codes.Unavailable {
		t.Fatalf("UpdateSession, redo unreachable: err = %v, want Unavailable", err)
	}
	rec.unreachable = false
	if len(rec.sessions) != 1 || len(watchedSessions(t, rec)) != 2 {
		t.Fatalf("a refused write was written: %d sessions", len(rec.sessions))
	}
}

// A session another writer changed between the read and the write (ABORTED)
// is read again and the change redone on top; one that keeps changing is
// ABORTED to the caller, nothing lost.
func TestSessionUpdateRedoneWhenRowChanged(t *testing.T) {
	rec := newFakeRecords()
	s := &Server{Sessions: NewSessionStore(rec)}
	ctx, jody := acmeCtx(), asUser("jody")
	created, err := s.CreateSession(ctx, jody, &pb.CreateSessionRequest{Tenant: acmeTid})
	if err != nil {
		t.Fatal(err)
	}
	rec.abort = maxWriteAttempts - 1
	if _, err := s.UpdateSession(ctx, jody, &pb.UpdateSessionRequest{Tenant: acmeTid, SessionId: created.GetSessionId(), Context: []string{"kept"}}); err != nil {
		t.Fatalf("UpdateSession after %d concurrent writes: %v", maxWriteAttempts-1, err)
	}
	if got := rec.sessions[created.GetSessionId()].GetContext(); len(got) != 1 || got[0] != "kept" {
		t.Fatalf("context = %v", got)
	}
	rec.abort = maxWriteAttempts
	if _, err := s.UpdateSession(ctx, jody, &pb.UpdateSessionRequest{Tenant: acmeTid, SessionId: created.GetSessionId(), Context: []string{"x"}}); status.Code(err) != codes.Aborted {
		t.Fatalf("UpdateSession losing every attempt: err = %v, want Aborted", err)
	}
}

func TestContextDocumentThroughWriteSurface(t *testing.T) {
	rec := newFakeRecords()
	rec.seed(acme, scopeUser, "jody", "old.md", "retired")
	s := &Server{Engine: &Engine{Contexts: NewContextStore(rec)}}
	ctx, jody := acmeCtx(), asUser("jody")

	// Write: one Apply puts both files and retires old.md.
	rec.takeTraces()
	files := []*pb.ContextFile{{Path: "b.md", Content: []byte("bee")}, {Path: "a.md", Content: []byte("ay")}}
	if _, err := s.UpdateContext(ctx, jody, &pb.UpdateContextRequest{Tenant: acmeTid, Files: files}); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.takeTraces()["Apply"]); n != 1 {
		t.Fatalf("%d Applys, want 1", n)
	}
	// Read: the rows, by another instance.
	got, err := NewContextStore(rec).Load(ctx, acme, "jody")
	if err != nil || !strings.Contains(got, "bee") || !strings.Contains(got, "ay") || strings.Contains(got, "retired") {
		t.Fatalf("Load = %q, %v", got, err)
	}
	// Watch: the seed, then the Apply in key order.
	w := watchedContextDocuments(t, rec)
	if len(w) != 4 {
		t.Fatalf("watched %d documents, want 4", len(w))
	}
	byPath := map[string]*pb.ContextDocument{}
	for _, d := range w[1:] {
		byPath[d.GetPath()] = d
	}
	if string(byPath["a.md"].GetContent()) != "ay" || string(byPath["b.md"].GetContent()) != "bee" || !byPath["old.md"].GetDeleted() {
		t.Fatalf("watched = %v", w)
	}

	// Redo unreachable: nothing of the replacement is written.
	rec.unreachable = true
	if _, err := s.UpdateContext(ctx, jody, &pb.UpdateContextRequest{Tenant: acmeTid, Files: []*pb.ContextFile{{Path: "c.md", Content: []byte("lost")}}}); status.Code(err) != codes.Unavailable {
		t.Fatalf("UpdateContext, redo unreachable: err = %v, want Unavailable", err)
	}
	if _, err := s.UpdateTenantContext(ctx, jody, &pb.UpdateTenantContextRequest{Tenant: acmeTid, Files: []*pb.ContextFile{{Path: "t.md"}}}); status.Code(err) != codes.Unavailable {
		t.Fatalf("UpdateTenantContext, redo unreachable: err = %v, want Unavailable", err)
	}
	rec.unreachable = false
	if live := rec.live(acme, scopeUser, "jody"); len(live) != 2 || live["a.md"] != "ay" {
		t.Fatalf("a refused replacement was written: %v", live)
	}
	if n := len(watchedContextDocuments(t, rec)); n != 4 {
		t.Fatalf("watched %d documents after the refusals, want 4", n)
	}
}

// A context too large for one Apply is written in batches, each within
// Apply's bound and in canonical order (the fake refuses any other), and a
// file of more than maxApplyBytes goes alone.
func TestContextReplaceBatchesApply(t *testing.T) {
	rec := newFakeRecords()
	c := NewContextStore(rec)
	var files []*pb.ContextFile
	for i := range maxApplyOps + 44 {
		files = append(files, &pb.ContextFile{Path: fmt.Sprintf("f%03d.md", i), Content: []byte("x")})
	}
	big := make([]byte, maxApplyBytes)
	files = append(files, &pb.ContextFile{Path: "big1.md", Content: big}, &pb.ContextFile{Path: "big2.md", Content: big})
	rec.takeTraces()
	if _, _, err := c.Replace(acmeCtx(), acme, scopeTenant, acme, files); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.takeTraces()["Apply"]); n < 3 {
		t.Fatalf("%d Applys for %d files and two of maxApplyBytes, want at least 3", n, len(files))
	}
	if live := rec.live(acme, scopeTenant, acme); len(live) != len(files) {
		t.Fatalf("%d rows live, want %d", len(live), len(files))
	}
}

func TestPanelRunThroughWriteSurface(t *testing.T) {
	a := &fakePanelist{model: "a-1", phase1: proposals("Build it on gRPC"), phase2: ranking(90, "", "Build it on gRPC")}
	panel, rec := newPanel(t, a)
	s := &Server{Panel: panel, Panels: NewPanelStore(rec)}
	ctx, alice := acmeCtx(), asUser("alice")
	ask := &pb.GeneratePanelAnswerRequest{Tenant: acmeTid, Question: grpcQuestion.Question, Situation: grpcQuestion.Situation}

	// Write: the run is inserted, then a decision rewrites it.
	resp, err := s.GeneratePanelAnswer(ctx, alice, ask)
	if err != nil {
		t.Fatal(err)
	}
	id, option := resp.GetPanelId(), resp.GetResult().GetTopOptionId()
	if _, err := s.UpdatePanelDecision(ctx, alice, &pb.UpdatePanelDecisionRequest{Tenant: acmeTid, PanelId: id, OptionId: option}); err != nil {
		t.Fatal(err)
	}
	// Read: the rows, by another instance.
	runs, err := NewPanelStore(rec).List(ctx, acme)
	if err != nil || len(runs) != 1 || runs[0].GetPanelId() != id || runs[0].GetDecidedOptionId() != option {
		t.Fatalf("runs read back = %v, %v", runs, err)
	}
	// Watch: the insert, then the decision.
	w := watchedPanelRuns(t, rec)
	if len(w) != 2 || w[0].GetPanelId() != id || w[0].GetDecidedOptionId() != "" || w[1].GetDecidedOptionId() != option {
		t.Fatalf("watched = %v", w)
	}

	// Redo unreachable: neither a run nor a decision is written.
	rec.unreachable = true
	if _, err := s.GeneratePanelAnswer(ctx, alice, ask); status.Code(err) != codes.Unavailable {
		t.Fatalf("GeneratePanelAnswer, redo unreachable: err = %v, want Unavailable", err)
	}
	if _, err := s.UpdatePanelDecision(ctx, alice, &pb.UpdatePanelDecisionRequest{Tenant: acmeTid, PanelId: id, OptionId: option}); status.Code(err) != codes.Unavailable {
		t.Fatalf("UpdatePanelDecision, redo unreachable: err = %v, want Unavailable", err)
	}
	rec.unreachable = false
	if len(rec.panelRuns) != 1 || len(watchedPanelRuns(t, rec)) != 2 {
		t.Fatalf("a refused write was written: %d runs", len(rec.panelRuns))
	}
}

// A store refusal reaches the caller as what it is: a durability the store
// cannot give yet (FAILED_PRECONDITION, as for REMOTE until redo#1 step 2)
// and a row that kept changing (ABORTED) as they are, the redo unreachable as
// UNAVAILABLE; anything else is the handler's own answer.
func TestStoreErrorKeepsTheStoresAnswer(t *testing.T) {
	for _, c := range []struct {
		store, want codes.Code
	}{
		{codes.Unavailable, codes.Unavailable},
		{codes.FailedPrecondition, codes.FailedPrecondition},
		{codes.Aborted, codes.Aborted},
		{codes.Internal, codes.Internal},
		{codes.AlreadyExists, codes.Internal},
	} {
		err := fmt.Errorf("writing: %w", status.Error(c.store, "refused"))
		if got := status.Code(storeError(err, codes.Internal)); got != c.want {
			t.Errorf("store %v: handler answers %v, want %v", c.store, got, c.want)
		}
	}
}

// Every store call is routed to the request's own tenant: the fake refuses
// any other, so a request naming another tenant writes nothing.
func TestStoreCallsRouteToTheRequestsTenant(t *testing.T) {
	rec := newFakeRecords()
	s := &Server{Sessions: NewSessionStore(rec)}
	if _, err := s.CreateSession(acmeCtx(), asUser("jody"), &pb.CreateSessionRequest{Tenant: "other001"}); status.Code(err) != codes.Internal || len(rec.sessions) != 0 {
		t.Fatalf("CreateSession routed to another tenant: err = %v, %d sessions", err, len(rec.sessions))
	}
	created, err := s.CreateSession(acmeCtx(), asUser("jody"), &pb.CreateSessionRequest{Tenant: acmeTid})
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.sessions[created.GetSessionId()]; got.GetTenantNamespace() != acme {
		t.Fatalf("stored = %v", got)
	}
}
