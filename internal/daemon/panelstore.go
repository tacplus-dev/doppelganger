// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	query_pb "github.com/tacplus-dev/doppelganger/tacplus/types/vservices/query"
)

// panelRecords is the generated record plane's S2S half over PanelRun:
// stratum's write surface, its point read and the direct read of rows.
type panelRecords interface {
	GetPanelRun(ctx context.Context, req *query_pb.GetPanelRunRequest, opts ...grpc.CallOption) (*query_pb.GetPanelRunResponse, error)
	InsertIfAbsentPanelRun(ctx context.Context, req *query_pb.InsertIfAbsentPanelRunRequest, opts ...grpc.CallOption) (*query_pb.RecordWriteResult, error)
	ReplaceIfUnchangedPanelRun(ctx context.Context, req *query_pb.ReplaceIfUnchangedPanelRunRequest, opts ...grpc.CallOption) (*query_pb.RecordWriteResult, error)
	// The plane's direct read: filter in, rows out, no result set.
	GetPanelRunRows(ctx context.Context, req *query_pb.GetPanelRunRowsRequest, opts ...grpc.CallOption) (*query_pb.GetPanelRunRowsResponse, error)
}

var (
	ErrPanelRunNotFound = errors.New("panel run not found")
	errUnknownOption    = errors.New("not an option of this panel run")
)

// PanelStore keeps panel runs as PanelRun records in stratum, the evidence the
// track record is read from: a new run is inserted, a decision rewrites the
// whole run on the revision it read, as SessionStore does.
type PanelStore struct {
	records panelRecords
	now     func() time.Time
}

func NewPanelStore(records panelRecords) *PanelStore {
	return &PanelStore{records: records, now: time.Now}
}

// Create keeps a new run for owner in tenant and gives it its id.
func (s *PanelStore) Create(ctx context.Context, owner, tenant string, run *pb.PanelRun) error {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	run.PanelId = hex.EncodeToString(buf)
	run.Owner = owner
	run.TenantNamespace = tenant
	run.CreatedAtMs = s.now().UnixMilli()
	s.stamp(ctx, run)
	_, err := s.records.InsertIfAbsentPanelRun(ctx, &query_pb.InsertIfAbsentPanelRunRequest{
		Item:         run,
		TargetTenant: targetTenant(ctx),
		TraceId:      traceID(ctx),
	})
	if err != nil {
		// ALREADY_EXISTS here is a 128-bit random id drawn twice: not retried.
		return fmt.Errorf("inserting panel run %s: %w", run.GetPanelId(), err)
	}
	return nil
}

// Decide records the option the person chose. A run is the tenant's, whoever
// asked it: the decision is the person's, not the asker's. A concurrent write
// of the run (ABORTED) is read and redone, up to maxWriteAttempts.
func (s *PanelStore) Decide(ctx context.Context, tenant, id, optionID string) error {
	for attempt := 1; ; attempt++ {
		run, revision, err := s.read(ctx, tenant, id)
		if err != nil {
			return err
		}
		found := false
		for _, o := range run.GetOptions() {
			found = found || o.GetOptionId() == optionID
		}
		if !found {
			return fmt.Errorf("%w: %q", errUnknownOption, optionID)
		}
		run.DecidedOptionId = optionID
		run.DecidedAtMs = s.now().UnixMilli()
		s.stamp(ctx, run)
		_, err = s.records.ReplaceIfUnchangedPanelRun(ctx, &query_pb.ReplaceIfUnchangedPanelRunRequest{
			Item:         run,
			Expected:     &query_pb.ReplaceIfUnchangedPanelRunRequest_ExpectedRevision{ExpectedRevision: revision},
			TargetTenant: targetTenant(ctx),
			TraceId:      traceID(ctx),
		})
		switch {
		case err == nil:
			return nil
		case status.Code(err) == codes.Aborted && attempt < maxWriteAttempts:
			continue
		case status.Code(err) == codes.NotFound:
			return ErrPanelRunNotFound
		default:
			return fmt.Errorf("writing panel run %s: %w", id, err)
		}
	}
}

// List is every live run of the tenant, as the store has them.
func (s *PanelStore) List(ctx context.Context, tenant string) ([]*pb.PanelRun, error) {
	resp, err := s.records.GetPanelRunRows(ctx, &query_pb.GetPanelRunRowsRequest{
		Filter:       &query_pb.PanelRunFilter{TenantNamespace: &tenant},
		Limit:        maxPanelRuns,
		TargetTenant: targetTenant(ctx),
		TraceId:      traceID(ctx),
	})
	if err != nil {
		return nil, fmt.Errorf("reading PanelRun rows: %w", err)
	}
	if resp.GetTruncated() {
		// Scoring part of the record would be a wrong track record.
		return nil, fmt.Errorf("tenant %s holds %d panel runs, more than the %d a read returns", tenant, resp.GetTotalCount(), maxPanelRuns)
	}
	return resp.GetItems(), nil
}

// maxPanelRuns bounds the track record's read: the direct read's cap.
const maxPanelRuns = 1000

// stamp marks run written now under the trace_id of the request changing it,
// which for a decision is not the one the run was asked under.
func (s *PanelStore) stamp(ctx context.Context, run *pb.PanelRun) {
	run.UpdatedAtMs = s.now().UnixMilli()
	run.TraceId = traceID(ctx)
}

// read is the run id of tenant and the revision it is at; a malformed id, a
// run that is not there or another tenant's is ErrPanelRunNotFound.
func (s *PanelStore) read(ctx context.Context, tenant, id string) (*pb.PanelRun, int64, error) {
	if !sessionIDPattern.MatchString(id) {
		return nil, 0, ErrPanelRunNotFound
	}
	resp, err := s.records.GetPanelRun(ctx, &query_pb.GetPanelRunRequest{
		PanelId:         id,
		TenantNamespace: tenant,
		TargetTenant:    targetTenant(ctx),
		TraceId:         traceID(ctx),
	})
	if status.Code(err) == codes.NotFound {
		return nil, 0, ErrPanelRunNotFound
	}
	if err != nil {
		return nil, 0, fmt.Errorf("reading panel run %s: %w", id, err)
	}
	run := resp.GetItem()
	if run == nil || run.GetDeleted() || run.GetTenantNamespace() != tenant {
		return nil, 0, ErrPanelRunNotFound
	}
	return run, resp.GetRevision(), nil
}
