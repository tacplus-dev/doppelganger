// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"iter"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	query_pb "github.com/tacplus-dev/doppelganger/tacplus/types/vservices/query"
)

// fakeRecords stands in for acme's stratum behind the generated record plane,
// as its write surface answers (tacplus USER.md, "Record write surface"):
// every entry carries a revision, increasing per write; InsertIfAbsent of a
// present key is ALREADY_EXISTS; ReplaceIfUnchanged of an absent key is
// NOT_FOUND and of a changed one ABORTED; Get of an absent key is NOT_FOUND;
// Apply takes its ops in canonical order only, all or nothing. A call routed
// to no tenant or another tenant's instance is refused, as the generated
// client and the instance refuse it. While unreachable is set the redo cannot
// be reached: every write is refused UNAVAILABLE and nothing is written.
// Watch<T>Changes delivers every acked write after from_revision, in order.
// The direct read lists the live rows, as the real plane does with
// include_deleted unset. Every call keeps the trace_id it carried, by name.
type fakeRecords struct {
	mu          sync.Mutex
	rows        map[string]*pb.ContextDocument
	sessions    map[string]*pb.Session
	panelRuns   map[string]*pb.PanelRun
	revisions   map[string]int64 // by type name and key
	revision    int64
	changes     []fakeChange
	unreachable bool
	// abort is how many more ReplaceIfUnchanged calls answer ABORTED, as
	// when another writer got to the row first.
	abort  int
	traces map[string][]string
}

// fakeChange is one acked write, as Watch<T>Changes delivers it.
type fakeChange struct {
	revision int64
	item     proto.Message
}

func newFakeRecords() *fakeRecords {
	return &fakeRecords{
		rows:      map[string]*pb.ContextDocument{},
		sessions:  map[string]*pb.Session{},
		panelRuns: map[string]*pb.PanelRun{},
		revisions: map[string]int64{},
		traces:    map[string][]string{},
	}
}

// acmeCtx is a request's context for acme's tid, as a handler makes it.
func acmeCtx() context.Context { return withTargetTenant(context.Background(), acmeTid) }

// takeTraces is the trace_id of every call since the last take, by call
// name, and forgets them.
func (f *fakeRecords) takeTraces() map[string][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.traces
	f.traces = map[string][]string{}
	return out
}

// call records a call and refuses one not routed to acme; f.mu is held.
func (f *fakeRecords) call(name, target, trace string) error {
	f.traces[name] = append(f.traces[name], trace)
	switch target {
	case "":
		return status.Error(codes.InvalidArgument, "route field target_tenant is empty")
	case acmeTid:
		return nil
	}
	return status.Errorf(codes.PermissionDenied, "routed to tenant %q, not this instance's", target)
}

// write is the write gate: the redo must be reachable; f.mu is held.
func (f *fakeRecords) write() error {
	if f.unreachable {
		return status.Error(codes.Unavailable, "redo unreachable: write not acked, not applied")
	}
	return nil
}

// commit stores item at key under a new revision; f.mu is held.
func (f *fakeRecords) commit(key string, item proto.Message) int64 {
	f.revision++
	f.revisions[key] = f.revision
	f.changes = append(f.changes, fakeChange{revision: f.revision, item: proto.Clone(item)})
	return f.revision
}

func (f *fakeRecords) result(rev int64, trace string) *query_pb.RecordWriteResult {
	return &query_pb.RecordWriteResult{Revision: rev, Durability: query_pb.RecordDurability_RECORD_DURABILITY_LOCAL, TraceId: trace}
}

// replaceable answers whether key may be replaced at expected; f.mu is held.
func (f *fakeRecords) replaceable(key string, present bool, expected int64) error {
	if !present {
		return status.Error(codes.NotFound, "no entry")
	}
	if f.abort > 0 {
		f.abort--
		f.revision++
		f.revisions[key] = f.revision
	}
	if f.revisions[key] != expected {
		return status.Error(codes.Aborted, "entry changed underneath")
	}
	return nil
}

// --- Session ---

func (f *fakeRecords) GetSession(_ context.Context, req *query_pb.GetSessionRequest, _ ...grpc.CallOption) (*query_pb.GetSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetSession", req.GetTargetTenant(), req.GetTraceId()); err != nil {
		return nil, err
	}
	s, ok := f.sessions[req.GetSessionId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "no session")
	}
	return &query_pb.GetSessionResponse{Item: proto.Clone(s).(*pb.Session), Revision: f.revisions["Session/"+s.GetSessionId()]}, nil
}

func (f *fakeRecords) InsertIfAbsentSession(_ context.Context, req *query_pb.InsertIfAbsentSessionRequest, _ ...grpc.CallOption) (*query_pb.RecordWriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("InsertIfAbsentSession", req.GetTargetTenant(), req.GetTraceId()); err != nil {
		return nil, err
	}
	if err := f.write(); err != nil {
		return nil, err
	}
	id := req.GetItem().GetSessionId()
	if _, ok := f.sessions[id]; ok {
		return nil, status.Error(codes.AlreadyExists, "session exists")
	}
	f.sessions[id] = proto.Clone(req.GetItem()).(*pb.Session)
	return f.result(f.commit("Session/"+id, req.GetItem()), req.GetTraceId()), nil
}

func (f *fakeRecords) ReplaceIfUnchangedSession(_ context.Context, req *query_pb.ReplaceIfUnchangedSessionRequest, _ ...grpc.CallOption) (*query_pb.RecordWriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ReplaceIfUnchangedSession", req.GetTargetTenant(), req.GetTraceId()); err != nil {
		return nil, err
	}
	if err := f.write(); err != nil {
		return nil, err
	}
	id := req.GetItem().GetSessionId()
	_, ok := f.sessions[id]
	if err := f.replaceable("Session/"+id, ok, req.GetExpectedRevision()); err != nil {
		return nil, err
	}
	f.sessions[id] = proto.Clone(req.GetItem()).(*pb.Session)
	return f.result(f.commit("Session/"+id, req.GetItem()), req.GetTraceId()), nil
}

func (f *fakeRecords) WatchSessionChanges(_ context.Context, req *query_pb.WatchSessionChangesRequest, _ ...grpc.CallOption) (iter.Seq2[*query_pb.SessionWatchEvent, error], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("WatchSessionChanges", req.GetTargetTenant(), req.GetTraceId()); err != nil {
		return nil, err
	}
	var events []*query_pb.SessionWatchEvent
	for _, c := range f.changes {
		if c.revision <= req.GetFromRevision() {
			continue
		}
		if s, ok := c.item.(*pb.Session); ok {
			events = append(events, &query_pb.SessionWatchEvent{Event: &query_pb.SessionWatchEvent_Delta{Delta: &query_pb.SessionWatchChange{
				Type: query_pb.RecordChangeType_RECORD_CHANGE_TYPE_MODIFY, Item: s, Revision: c.revision,
			}}})
		}
	}
	return seqOf(events), nil
}

// --- PanelRun ---

func (f *fakeRecords) GetPanelRun(_ context.Context, req *query_pb.GetPanelRunRequest, _ ...grpc.CallOption) (*query_pb.GetPanelRunResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetPanelRun", req.GetTargetTenant(), req.GetTraceId()); err != nil {
		return nil, err
	}
	r, ok := f.panelRuns[req.GetPanelId()]
	if !ok || (req.GetTenantNamespace() != "" && r.GetTenantNamespace() != req.GetTenantNamespace()) {
		return nil, status.Error(codes.NotFound, "no panel run")
	}
	return &query_pb.GetPanelRunResponse{Item: proto.Clone(r).(*pb.PanelRun), Revision: f.revisions["PanelRun/"+r.GetPanelId()]}, nil
}

func (f *fakeRecords) InsertIfAbsentPanelRun(_ context.Context, req *query_pb.InsertIfAbsentPanelRunRequest, _ ...grpc.CallOption) (*query_pb.RecordWriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("InsertIfAbsentPanelRun", req.GetTargetTenant(), req.GetTraceId()); err != nil {
		return nil, err
	}
	if err := f.write(); err != nil {
		return nil, err
	}
	id := req.GetItem().GetPanelId()
	if _, ok := f.panelRuns[id]; ok {
		return nil, status.Error(codes.AlreadyExists, "panel run exists")
	}
	f.panelRuns[id] = proto.Clone(req.GetItem()).(*pb.PanelRun)
	return f.result(f.commit("PanelRun/"+id, req.GetItem()), req.GetTraceId()), nil
}

func (f *fakeRecords) ReplaceIfUnchangedPanelRun(_ context.Context, req *query_pb.ReplaceIfUnchangedPanelRunRequest, _ ...grpc.CallOption) (*query_pb.RecordWriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ReplaceIfUnchangedPanelRun", req.GetTargetTenant(), req.GetTraceId()); err != nil {
		return nil, err
	}
	if err := f.write(); err != nil {
		return nil, err
	}
	id := req.GetItem().GetPanelId()
	_, ok := f.panelRuns[id]
	if err := f.replaceable("PanelRun/"+id, ok, req.GetExpectedRevision()); err != nil {
		return nil, err
	}
	f.panelRuns[id] = proto.Clone(req.GetItem()).(*pb.PanelRun)
	return f.result(f.commit("PanelRun/"+id, req.GetItem()), req.GetTraceId()), nil
}

func (f *fakeRecords) WatchPanelRunChanges(_ context.Context, req *query_pb.WatchPanelRunChangesRequest, _ ...grpc.CallOption) (iter.Seq2[*query_pb.PanelRunWatchEvent, error], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("WatchPanelRunChanges", req.GetTargetTenant(), req.GetTraceId()); err != nil {
		return nil, err
	}
	var events []*query_pb.PanelRunWatchEvent
	for _, c := range f.changes {
		if c.revision <= req.GetFromRevision() {
			continue
		}
		if r, ok := c.item.(*pb.PanelRun); ok {
			events = append(events, &query_pb.PanelRunWatchEvent{Event: &query_pb.PanelRunWatchEvent_Delta{Delta: &query_pb.PanelRunWatchChange{
				Type: query_pb.RecordChangeType_RECORD_CHANGE_TYPE_MODIFY, Item: r, Revision: c.revision,
			}}})
		}
	}
	return seqOf(events), nil
}

func (f *fakeRecords) GetPanelRunRows(_ context.Context, req *query_pb.GetPanelRunRowsRequest, _ ...grpc.CallOption) (*query_pb.GetPanelRunRowsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetPanelRunRows", req.GetTargetTenant(), req.GetTraceId()); err != nil {
		return nil, err
	}
	filter := req.GetFilter()
	var out []*pb.PanelRun
	for _, r := range f.panelRuns {
		if r.GetDeleted() && !req.GetIncludeDeleted() {
			continue
		}
		if filter.PanelId != nil && r.GetPanelId() != *filter.PanelId {
			continue
		}
		if filter.TenantNamespace != nil && r.GetTenantNamespace() != *filter.TenantNamespace {
			continue
		}
		if filter.Owner != nil && r.GetOwner() != *filter.Owner {
			continue
		}
		out = append(out, proto.Clone(r).(*pb.PanelRun))
	}
	r := rowsOf(out, req.GetLimit())
	return &query_pb.GetPanelRunRowsResponse{Items: r.items, TotalCount: r.total, Truncated: r.truncated}, nil
}

// --- ContextDocument ---

// Apply takes Put ops of ContextDocument (all this service sends), in the
// canonical order (by key, each once), at most 256, all or nothing.
func (f *fakeRecords) Apply(_ context.Context, req *query_pb.RecordApplyRequest, _ ...grpc.CallOption) (*query_pb.RecordApplyResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("Apply", req.GetTargetTenant(), req.GetTraceId()); err != nil {
		return nil, err
	}
	if n := len(req.GetOps()); n < 1 || n > maxApplyOps {
		return nil, status.Errorf(codes.InvalidArgument, "%d ops", n)
	}
	last := ""
	for i, op := range req.GetOps() {
		put := op.GetPutContextDocument()
		if put == nil {
			return nil, status.Errorf(codes.InvalidArgument, "op %d is not a PutContextDocument", i)
		}
		if put.GetTargetTenant() != req.GetTargetTenant() {
			return nil, status.Errorf(codes.InvalidArgument, "op %d routed to %q, the Apply to %q", i, put.GetTargetTenant(), req.GetTargetTenant())
		}
		if key := put.GetItem().GetDocumentId(); i > 0 && key <= last {
			return nil, status.Errorf(codes.InvalidArgument, "op %d out of canonical order", i)
		} else {
			last = key
		}
	}
	if err := f.write(); err != nil {
		return nil, err
	}
	resp := &query_pb.RecordApplyResponse{TraceId: req.GetTraceId()}
	for _, op := range req.GetOps() {
		d := op.GetPutContextDocument().GetItem()
		f.rows[d.GetDocumentId()] = proto.Clone(d).(*pb.ContextDocument)
		resp.Results = append(resp.Results, f.result(f.commit("ContextDocument/"+d.GetDocumentId(), d), req.GetTraceId()))
	}
	return resp, nil
}

func (f *fakeRecords) WatchContextDocumentChanges(_ context.Context, req *query_pb.WatchContextDocumentChangesRequest, _ ...grpc.CallOption) (iter.Seq2[*query_pb.ContextDocumentWatchEvent, error], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("WatchContextDocumentChanges", req.GetTargetTenant(), req.GetTraceId()); err != nil {
		return nil, err
	}
	var events []*query_pb.ContextDocumentWatchEvent
	for _, c := range f.changes {
		if c.revision <= req.GetFromRevision() {
			continue
		}
		if d, ok := c.item.(*pb.ContextDocument); ok {
			events = append(events, &query_pb.ContextDocumentWatchEvent{Event: &query_pb.ContextDocumentWatchEvent_Delta{Delta: &query_pb.ContextDocumentWatchChange{
				Type: query_pb.RecordChangeType_RECORD_CHANGE_TYPE_MODIFY, Item: d, Revision: c.revision,
			}}})
		}
	}
	return seqOf(events), nil
}

// seed is a row already in the store, as an earlier upload left it.
func (f *fakeRecords) seed(tenant, scope, owner, path, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := &pb.ContextDocument{
		DocumentId: documentID(tenant, scope, owner, path), TenantNamespace: tenant, Scope: scope, Owner: owner,
		Path: path, Content: []byte(content),
	}
	f.rows[d.GetDocumentId()] = d
	f.commit("ContextDocument/"+d.GetDocumentId(), d)
}

func (f *fakeRecords) GetContextDocumentRows(_ context.Context, req *query_pb.GetContextDocumentRowsRequest, _ ...grpc.CallOption) (*query_pb.GetContextDocumentRowsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetContextDocumentRows", req.GetTargetTenant(), req.GetTraceId()); err != nil {
		return nil, err
	}
	filter := req.GetFilter()
	var out []*pb.ContextDocument
	for _, d := range f.rows {
		if d.GetDeleted() && !req.GetIncludeDeleted() {
			continue
		}
		if filter.TenantNamespace != nil && d.GetTenantNamespace() != *filter.TenantNamespace {
			continue
		}
		if filter.Scope != nil && d.GetScope() != *filter.Scope {
			continue
		}
		if filter.Owner != nil && d.GetOwner() != *filter.Owner {
			continue
		}
		out = append(out, proto.Clone(d).(*pb.ContextDocument))
	}
	r := rowsOf(out, req.GetLimit())
	return &query_pb.GetContextDocumentRowsResponse{Items: r.items, TotalCount: r.total, Truncated: r.truncated}, nil
}

// live is the store's live rows for one context, by path.
func (f *fakeRecords) live(tenant, scope, owner string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, d := range f.rows {
		if !d.GetDeleted() && d.GetTenantNamespace() == tenant && d.GetScope() == scope && d.GetOwner() == owner {
			out[d.GetPath()] = string(d.GetContent())
		}
	}
	return out
}

// rowsOf applies the plane's limit the way stratum does: default 100, at most
// 1000, total and truncated reported.
func rowsOf[T any](all []T, limit int32) *rows[T] {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	n := int(limit)
	if n > len(all) {
		n = len(all)
	}
	return &rows[T]{items: all[:n], total: int64(len(all)), truncated: n < len(all)}
}

type rows[T any] struct {
	items     []T
	total     int64
	truncated bool
}

// seqOf is a watch stream that has delivered events and ends.
func seqOf[T any](events []T) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for _, e := range events {
			if !yield(e, nil) {
				return
			}
		}
	}
}
