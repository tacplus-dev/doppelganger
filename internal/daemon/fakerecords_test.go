// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"fmt"
	"iter"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	query_pb "github.com/tacplus-dev/doppelganger/tacplus/types/vservices/query"
)

// fakeRecords stands in for stratum behind the generated record plane: a
// published ContextDocument lands on its row by document_id (a deleted
// publish marks it), and a query over (tenant, scope, owner) lists the live
// rows, as the real plane does with include_deleted unset.
type fakeRecords struct {
	mu       sync.Mutex
	rows     map[string]*pb.ContextDocument
	queries  map[string]*query_pb.ContextDocumentFilter
	sessions map[string]*pb.Session
	squeries map[string]*query_pb.SessionFilter
	next     int
}

func newFakeRecords() *fakeRecords {
	return &fakeRecords{
		rows: map[string]*pb.ContextDocument{}, queries: map[string]*query_pb.ContextDocumentFilter{},
		sessions: map[string]*pb.Session{}, squeries: map[string]*query_pb.SessionFilter{},
	}
}

func (f *fakeRecords) publishSession(_ context.Context, s *pb.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[s.GetSessionId()] = proto.Clone(s).(*pb.Session)
	return nil
}

func (f *fakeRecords) CreateSessionQuery(_ context.Context, req *query_pb.OpenSessionQueryRequest, _ ...grpc.CallOption) (*query_pb.QueryHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := fmt.Sprintf("s%d", f.next)
	f.squeries[id] = req.GetFilter()
	return &query_pb.QueryHandle{QueryId: id}, nil
}

func (f *fakeRecords) ListSession(_ context.Context, req *query_pb.PullSessionRequest, _ ...grpc.CallOption) iter.Seq2[*pb.Session, error] {
	return func(yield func(*pb.Session, error) bool) {
		f.mu.Lock()
		filter, ok := f.squeries[req.GetQueryId()]
		var out []*pb.Session
		if ok {
			for _, s := range f.sessions {
				if s.GetDeleted() {
					continue
				}
				if filter.SessionId != nil && s.GetSessionId() != *filter.SessionId {
					continue
				}
				if filter.Owner != nil && s.GetOwner() != *filter.Owner {
					continue
				}
				out = append(out, proto.Clone(s).(*pb.Session))
			}
		}
		f.mu.Unlock()
		if !ok {
			yield(nil, fmt.Errorf("unknown query %q", req.GetQueryId()))
			return
		}
		for _, s := range out {
			if !yield(s, nil) {
				return
			}
		}
	}
}

func (f *fakeRecords) DeleteSessionQuery(_ context.Context, req *query_pb.QueryHandle, _ ...grpc.CallOption) (*query_pb.QueryStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.squeries, req.GetQueryId())
	return &query_pb.QueryStats{}, nil
}

func (f *fakeRecords) publish(_ context.Context, d *pb.ContextDocument) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[d.GetDocumentId()] = proto.Clone(d).(*pb.ContextDocument)
	return nil
}

// seed is a row already in the store, as an earlier upload left it.
func (f *fakeRecords) seed(tenant, scope, owner, path, content string) {
	_ = f.publish(context.Background(), &pb.ContextDocument{
		DocumentId: documentID(tenant, scope, owner, path), TenantNamespace: tenant, Scope: scope, Owner: owner,
		Path: path, Content: []byte(content),
	})
}

func (f *fakeRecords) CreateContextDocumentQuery(_ context.Context, req *query_pb.OpenContextDocumentQueryRequest, _ ...grpc.CallOption) (*query_pb.QueryHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := fmt.Sprintf("q%d", f.next)
	f.queries[id] = req.GetFilter()
	return &query_pb.QueryHandle{QueryId: id}, nil
}

func (f *fakeRecords) ListContextDocument(_ context.Context, req *query_pb.PullContextDocumentRequest, _ ...grpc.CallOption) iter.Seq2[*pb.ContextDocument, error] {
	return func(yield func(*pb.ContextDocument, error) bool) {
		f.mu.Lock()
		filter, ok := f.queries[req.GetQueryId()]
		var out []*pb.ContextDocument
		if ok {
			for _, d := range f.rows {
				if d.GetDeleted() {
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
		}
		f.mu.Unlock()
		if !ok {
			yield(nil, fmt.Errorf("unknown query %q", req.GetQueryId()))
			return
		}
		for _, d := range out {
			if !yield(d, nil) {
				return
			}
		}
	}
}

func (f *fakeRecords) DeleteContextDocumentQuery(_ context.Context, req *query_pb.QueryHandle, _ ...grpc.CallOption) (*query_pb.QueryStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.queries, req.GetQueryId())
	return &query_pb.QueryStats{}, nil
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
