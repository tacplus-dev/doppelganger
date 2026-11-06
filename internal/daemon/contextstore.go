// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

// contextRecords is the generated record plane's S2S half over
// ContextDocument, as RecordMapClient implements it; stratum serves it.
type contextRecords interface {
	// The plane's direct read: filter in, rows out, no result set, so nothing
	// a connection failover between two calls could lose.
	GetContextDocumentRows(ctx context.Context, req *query_pb.GetContextDocumentRowsRequest, opts ...grpc.CallOption) (*query_pb.GetContextDocumentRowsResponse, error)
	// The write surface's atomic batch.
	Apply(ctx context.Context, req *query_pb.RecordApplyRequest, opts ...grpc.CallOption) (*query_pb.RecordApplyResponse, error)
}

// ContextStore is the context this instance answers from, as ContextDocument
// records in stratum: the tenant's shared context (scope "tenant") and each
// user's own (scope "user"). Writing is Apply on stratum's write surface;
// reading is the record plane's direct read. Nothing is kept on the
// instance's disk, so nothing is lost with it.
type ContextStore struct {
	records contextRecords
	now     func() time.Time
}

func NewContextStore(records contextRecords) *ContextStore {
	return &ContextStore{records: records, now: time.Now}
}

var (
	errBadContextPath       = errors.New("context file path must be relative, clean and inside the context")
	errDuplicateContextPath = errors.New("context file path given twice")
)

// Replace makes files the whole content of one context: every file is put
// onto its own row (the same path lands on the same row), and the rows the
// context no longer has are put deleted.
func (c *ContextStore) Replace(ctx context.Context, tenant, scope, owner string, files []*pb.ContextFile) (int, int64, error) {
	seen := map[string]bool{}
	for _, f := range files {
		if !validContextPath(f.GetPath()) {
			return 0, 0, fmt.Errorf("%w: %q", errBadContextPath, f.GetPath())
		}
		if seen[f.GetPath()] {
			return 0, 0, fmt.Errorf("%w: %q", errDuplicateContextPath, f.GetPath())
		}
		seen[f.GetPath()] = true
	}
	existing, err := c.list(ctx, tenant, scope, owner)
	if err != nil {
		return 0, 0, err
	}
	now := c.now().UnixMilli()
	var bytes int64
	var docs []*pb.ContextDocument
	for _, f := range files {
		docs = append(docs, &pb.ContextDocument{
			DocumentId:      documentID(tenant, scope, owner, f.GetPath()),
			TenantNamespace: tenant,
			Scope:           scope,
			Owner:           owner,
			Path:            f.GetPath(),
			Content:         f.GetContent(),
			UpdatedAtMs:     now,
			TraceId:         traceID(ctx),
		})
		bytes += int64(len(f.GetContent()))
	}
	for _, old := range existing {
		if seen[old.GetPath()] {
			continue
		}
		docs = append(docs, &pb.ContextDocument{
			DocumentId:      old.GetDocumentId(),
			TenantNamespace: tenant,
			Scope:           scope,
			Owner:           owner,
			Path:            old.GetPath(),
			UpdatedAtMs:     now,
			Deleted:         true,
			TraceId:         traceID(ctx),
		})
	}
	if err := c.put(ctx, docs); err != nil {
		return 0, 0, err
	}
	return len(files), bytes, nil
}

// put writes docs with Apply, in the canonical lock order Apply requires (one
// record type, so by key). A context that fits one Apply (maxApplyOps rows,
// maxApplyBytes of content) is replaced atomically; a larger one in batches,
// each atomic, and a failure part way leaves a mix the caller's retry of the
// same replacement (it is idempotent) completes.
func (c *ContextStore) put(ctx context.Context, docs []*pb.ContextDocument) error {
	sort.Slice(docs, func(i, j int) bool { return docs[i].GetDocumentId() < docs[j].GetDocumentId() })
	for len(docs) > 0 {
		n, size := 0, 0
		for n < len(docs) && n < maxApplyOps && (n == 0 || size+len(docs[n].GetContent()) <= maxApplyBytes) {
			size += len(docs[n].GetContent())
			n++
		}
		req := &query_pb.RecordApplyRequest{TargetTenant: targetTenant(ctx), TraceId: traceID(ctx)}
		for _, d := range docs[:n] {
			req.Ops = append(req.Ops, &query_pb.RecordOp{
				Op: &query_pb.RecordOp_PutContextDocument{PutContextDocument: &query_pb.PutContextDocumentRequest{
					Item:         d,
					TargetTenant: targetTenant(ctx),
					TraceId:      traceID(ctx),
				}},
				TraceId: traceID(ctx),
			})
		}
		if _, err := c.records.Apply(ctx, req); err != nil {
			return fmt.Errorf("writing %d context documents: %w", n, err)
		}
		docs = docs[n:]
	}
	return nil
}

// maxApplyOps is the most ops one Apply takes (its generated bound), and
// maxApplyBytes the most file content this service sends in one, well under
// a gRPC message; a larger file goes alone.
const (
	maxApplyOps   = 256
	maxApplyBytes = 2 << 20
)

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

// Principles renders the tenant context's Markdown files under paths (a file,
// or a directory and everything in it) and nothing else: all of the context a
// panelist is given. No paths, or none matching, is errNoPrinciples: the
// panel fails closed rather than answer without the person's principles.
func (c *ContextStore) Principles(ctx context.Context, tenant string, paths []string) (string, error) {
	if len(paths) == 0 {
		return "", errNoPrinciples
	}
	docs, err := c.list(ctx, tenant, scopeTenant, tenant)
	if err != nil {
		return "", err
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].GetPath() < docs[j].GetPath() })
	var b strings.Builder
	for _, d := range docs {
		if strings.HasSuffix(d.GetPath(), ".md") && underAny(d.GetPath(), paths) {
			fmt.Fprintf(&b, "<file path=%q>\n%s\n</file>\n\n", d.GetPath(), d.GetContent())
		}
	}
	if b.Len() == 0 {
		return "", errNoPrinciples
	}
	return b.String(), nil
}

// underAny reports whether path is one of prefixes or inside one of them as a
// directory ("principles" and "principles/" both hold "principles/x.md", not
// "principles-old/x.md").
func underAny(path string, prefixes []string) bool {
	for _, p := range prefixes {
		p = strings.TrimSuffix(p, "/")
		if p != "" && (path == p || strings.HasPrefix(path, p+"/")) {
			return true
		}
	}
	return false
}

// list reads one context's live rows: a point query on (tenant, scope,
// owner) with the direct read.
func (c *ContextStore) list(ctx context.Context, tenant, scope, owner string) ([]*pb.ContextDocument, error) {
	resp, err := c.records.GetContextDocumentRows(ctx, &query_pb.GetContextDocumentRowsRequest{
		Filter:       &query_pb.ContextDocumentFilter{TenantNamespace: &tenant, Scope: &scope, Owner: &owner},
		Limit:        maxContextDocuments,
		TargetTenant: targetTenant(ctx),
		TraceId:      traceID(ctx),
	})
	if err != nil {
		return nil, fmt.Errorf("reading ContextDocument rows: %w", err)
	}
	if resp.GetTruncated() {
		// A context larger than the direct read's bound is a result set's job;
		// answering from part of it would be answering from the wrong context.
		return nil, fmt.Errorf("context %s/%s/%s holds %d documents, more than the %d a read returns", tenant, scope, owner, resp.GetTotalCount(), maxContextDocuments)
	}
	return resp.GetItems(), nil
}

// maxContextDocuments bounds one context's read: the direct read's cap, and
// more files than any doppelganger context has needed.
const maxContextDocuments = 1000

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
