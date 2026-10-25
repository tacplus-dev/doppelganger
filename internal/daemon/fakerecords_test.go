// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
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
	sessions map[string]*pb.Session
}

func newFakeRecords() *fakeRecords {
	return &fakeRecords{
		rows:     map[string]*pb.ContextDocument{},
		sessions: map[string]*pb.Session{},
	}
}

func (f *fakeRecords) publishSession(_ context.Context, s *pb.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[s.GetSessionId()] = proto.Clone(s).(*pb.Session)
	return nil
}

func (f *fakeRecords) GetSessionRows(_ context.Context, req *query_pb.GetSessionRowsRequest, _ ...grpc.CallOption) (*query_pb.GetSessionRowsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	filter := req.GetFilter()
	var out []*pb.Session
	for _, s := range f.sessions {
		if s.GetDeleted() && !req.GetIncludeDeleted() {
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
	r := rowsOf(out, req.GetLimit())
	return &query_pb.GetSessionRowsResponse{Items: r.items, TotalCount: r.total, Truncated: r.truncated}, nil
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

func (f *fakeRecords) GetContextDocumentRows(_ context.Context, req *query_pb.GetContextDocumentRowsRequest, _ ...grpc.CallOption) (*query_pb.GetContextDocumentRowsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
