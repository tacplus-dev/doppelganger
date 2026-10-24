// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc"

	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	query_pb "github.com/tacplus-dev/doppelganger/tacplus/types/vservices/query"
)

const (
	scopeTenant = "tenant"
	scopeUser   = "user"
)

// contextRecords is the generated record query plane's S2S half over
// ContextDocument, as RecordMapClient implements it; stratum serves it.
type contextRecords interface {
	CreateContextDocumentQuery(ctx context.Context, req *query_pb.OpenContextDocumentQueryRequest, opts ...grpc.CallOption) (*query_pb.QueryHandle, error)
	ListContextDocument(ctx context.Context, req *query_pb.PullContextDocumentRequest, opts ...grpc.CallOption) iter.Seq2[*pb.ContextDocument, error]
	DeleteContextDocumentQuery(ctx context.Context, req *query_pb.QueryHandle, opts ...grpc.CallOption) (*query_pb.QueryStats, error)
}

// ContextStore is the context this instance answers from, as ContextDocument
// records in stratum: the tenant's shared context (scope "tenant") and each
// user's own (scope "user"). Writing is publishing the record (stratum stores
// it on its subscription); reading is the record query plane. Nothing is kept
// on the instance's disk, so nothing is lost with it.
type ContextStore struct {
	records contextRecords
	publish func(context.Context, *pb.ContextDocument) error
	now     func() time.Time
}

func NewContextStore(records contextRecords, publish func(context.Context, *pb.ContextDocument) error) *ContextStore {
	return &ContextStore{records: records, publish: publish, now: time.Now}
}

var errBadContextPath = errors.New("context file path must be relative, clean and inside the context")

// Replace makes files the whole content of one context: every file is
// published onto its own row (the same path lands on the same row), and the
// rows the context no longer has are published deleted.
func (c *ContextStore) Replace(ctx context.Context, tenant, scope, owner string, files []*pb.ContextFile) (int, int64, error) {
	for _, f := range files {
		if !validContextPath(f.GetPath()) {
			return 0, 0, fmt.Errorf("%w: %q", errBadContextPath, f.GetPath())
		}
	}
	existing, err := c.list(ctx, tenant, scope, owner)
	if err != nil {
		return 0, 0, err
	}
	kept := map[string]bool{}
	now := c.now().UnixMilli()
	var bytes int64
	for _, f := range files {
		kept[f.GetPath()] = true
		doc := &pb.ContextDocument{
			DocumentId:      documentID(tenant, scope, owner, f.GetPath()),
			TenantNamespace: tenant,
			Scope:           scope,
			Owner:           owner,
			Path:            f.GetPath(),
			Content:         f.GetContent(),
			UpdatedAtMs:     now,
		}
		if err := c.publish(ctx, doc); err != nil {
			return 0, 0, fmt.Errorf("publishing context document %q: %w", f.GetPath(), err)
		}
		bytes += int64(len(f.GetContent()))
	}
	for _, old := range existing {
		if kept[old.GetPath()] {
			continue
		}
		gone := &pb.ContextDocument{
			DocumentId:      old.GetDocumentId(),
			TenantNamespace: tenant,
			Scope:           scope,
			Owner:           owner,
			Path:            old.GetPath(),
			UpdatedAtMs:     now,
			Deleted:         true,
		}
		if err := c.publish(ctx, gone); err != nil {
			return 0, 0, fmt.Errorf("retiring context document %q: %w", old.GetPath(), err)
		}
	}
	return len(files), bytes, nil
}

// Load renders the tenant's context and the user's own, Markdown files only,
// each tagged with its scope and path, in path order. No files in either is an
// error, because an answer from nothing is not an answer.
func (c *ContextStore) Load(ctx context.Context, tenant, user string) (string, error) {
	var b strings.Builder
	n := 0
	for _, scope := range []struct{ name, owner string }{{scopeTenant, tenant}, {scopeUser, user}} {
		docs, err := c.list(ctx, tenant, scope.name, scope.owner)
		if err != nil {
			return "", err
		}
		sort.Slice(docs, func(i, j int) bool { return docs[i].GetPath() < docs[j].GetPath() })
		wrote := false
		for _, d := range docs {
			if !strings.HasSuffix(d.GetPath(), ".md") {
				continue
			}
			if !wrote {
				fmt.Fprintf(&b, "## %s context\n\n", scope.name)
				wrote = true
			}
			fmt.Fprintf(&b, "<file path=%q>\n%s\n</file>\n\n", d.GetPath(), d.GetContent())
			n++
		}
	}
	if n == 0 {
		return "", errors.New("no context for this caller: the tenant has none and the user has uploaded none")
	}
	return b.String(), nil
}

// list reads one context's live rows: a point query on (tenant, scope, owner),
// paged through, and closed.
func (c *ContextStore) list(ctx context.Context, tenant, scope, owner string) ([]*pb.ContextDocument, error) {
	handle, err := c.records.CreateContextDocumentQuery(ctx, &query_pb.OpenContextDocumentQueryRequest{
		Filter: &query_pb.ContextDocumentFilter{TenantNamespace: &tenant, Scope: &scope, Owner: &owner},
	})
	if err != nil {
		return nil, fmt.Errorf("opening ContextDocument query: %w", err)
	}
	defer c.records.DeleteContextDocumentQuery(ctx, &query_pb.QueryHandle{QueryId: handle.GetQueryId()}) //nolint:errcheck
	var docs []*pb.ContextDocument
	for d, err := range c.records.ListContextDocument(ctx, &query_pb.PullContextDocumentRequest{QueryId: handle.GetQueryId(), PageSize: 256}) {
		if err != nil {
			return nil, fmt.Errorf("reading ContextDocument rows: %w", err)
		}
		docs = append(docs, d)
	}
	return docs, nil
}

// documentID is the row a file lands on: a stable digest of where it sits.
func documentID(tenant, scope, owner, path string) string {
	sum := sha256.Sum256([]byte(tenant + "\x00" + scope + "\x00" + owner + "\x00" + path))
	return "ctx-" + hex.EncodeToString(sum[:16])
}

// validContextPath admits a clean, relative, slash-separated path with no
// parent references.
func validContextPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean != p || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	return true
}
