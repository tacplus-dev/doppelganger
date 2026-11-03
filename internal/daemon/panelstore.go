// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	query_pb "github.com/tacplus-dev/doppelganger/tacplus/types/vservices/query"
)

// panelRecords is the generated record query plane's S2S half over PanelRun.
type panelRecords interface {
	// The plane's direct read: filter in, rows out, no result set.
	GetPanelRunRows(ctx context.Context, req *query_pb.GetPanelRunRowsRequest, opts ...grpc.CallOption) (*query_pb.GetPanelRunRowsResponse, error)
}

var (
	ErrPanelRunNotFound = errors.New("panel run not found")
	errUnknownOption    = errors.New("not an option of this panel run")
)

// PanelStore keeps panel runs as PanelRun records in stratum, the evidence the
// track record is read from: every change republishes the whole run; reads go
// to the store after the write-through cache of this instance's own recent
// publishes (recentTTL), as SessionStore does.
type PanelStore struct {
	records panelRecords
	publish func(context.Context, *pb.PanelRun) error
	now     func() time.Time

	mu     sync.Mutex
	recent map[string]recentPanelRun
}

type recentPanelRun struct {
	run     *pb.PanelRun
	expires time.Time
}

func NewPanelStore(records panelRecords, publish func(context.Context, *pb.PanelRun) error) *PanelStore {
	return &PanelStore{records: records, publish: publish, now: time.Now, recent: map[string]recentPanelRun{}}
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
	return s.write(ctx, run)
}

// Decide records the option the person chose. A run is the tenant's, whoever
// asked it: the decision is the person's, not the asker's.
func (s *PanelStore) Decide(ctx context.Context, tenant, id, optionID string) error {
	run, err := s.read(ctx, tenant, id)
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
	return s.write(ctx, run)
}

// List is every live run of the tenant: the store's rows, with this
// instance's recent publishes over them.
func (s *PanelStore) List(ctx context.Context, tenant string) ([]*pb.PanelRun, error) {
	resp, err := s.records.GetPanelRunRows(ctx, &query_pb.GetPanelRunRowsRequest{
		Filter:  &query_pb.PanelRunFilter{TenantNamespace: &tenant},
		Limit:   maxPanelRuns,
		TraceId: traceID(ctx),
	})
	if err != nil {
		return nil, fmt.Errorf("reading PanelRun rows: %w", err)
	}
	if resp.GetTruncated() {
		// Scoring part of the record would be a wrong track record.
		return nil, fmt.Errorf("tenant %s holds %d panel runs, more than the %d a read returns", tenant, resp.GetTotalCount(), maxPanelRuns)
	}
	byID := map[string]*pb.PanelRun{}
	var order []string
	for _, r := range resp.GetItems() {
		byID[r.GetPanelId()] = r
		order = append(order, r.GetPanelId())
	}
	s.mu.Lock()
	s.expire()
	for id, r := range s.recent {
		if r.run.GetTenantNamespace() != tenant {
			continue
		}
		if _, ok := byID[id]; !ok {
			order = append(order, id)
		}
		byID[id] = proto.Clone(r.run).(*pb.PanelRun)
	}
	s.mu.Unlock()
	runs := make([]*pb.PanelRun, 0, len(order))
	for _, id := range order {
		runs = append(runs, byID[id])
	}
	return runs, nil
}

// maxPanelRuns bounds the track record's read: the direct read's cap.
const maxPanelRuns = 1000

// write publishes the whole run under the trace_id of the request changing
// it, which for a decision is not the one the run was asked under.
func (s *PanelStore) write(ctx context.Context, run *pb.PanelRun) error {
	run.UpdatedAtMs = s.now().UnixMilli()
	run.TraceId = traceID(ctx)
	if err := s.publish(ctx, run); err != nil {
		return fmt.Errorf("publishing panel run %s: %w", run.GetPanelId(), err)
	}
	s.mu.Lock()
	s.recent[run.GetPanelId()] = recentPanelRun{run: proto.Clone(run).(*pb.PanelRun), expires: s.now().Add(recentTTL)}
	s.mu.Unlock()
	return nil
}

// expire drops the cache entries past recentTTL; s.mu is held.
func (s *PanelStore) expire() {
	now := s.now()
	for k, r := range s.recent {
		if !now.Before(r.expires) {
			delete(s.recent, k)
		}
	}
}

// read is this instance's recent publish of id if it has one, else the
// store's row; a run of another tenant is not found.
func (s *PanelStore) read(ctx context.Context, tenant, id string) (*pb.PanelRun, error) {
	if !sessionIDPattern.MatchString(id) {
		return nil, ErrPanelRunNotFound
	}
	s.mu.Lock()
	s.expire()
	r, ok := s.recent[id]
	s.mu.Unlock()
	run := r.run
	if ok {
		run = proto.Clone(run).(*pb.PanelRun)
	} else {
		// Limit 2: the key is unique, so a second row means the read cannot be trusted.
		resp, err := s.records.GetPanelRunRows(ctx, &query_pb.GetPanelRunRowsRequest{
			Filter:  &query_pb.PanelRunFilter{PanelId: &id},
			Limit:   2,
			TraceId: traceID(ctx),
		})
		if err != nil {
			return nil, fmt.Errorf("reading PanelRun row: %w", err)
		}
		switch len(resp.GetItems()) {
		case 0:
			return nil, ErrPanelRunNotFound
		case 1:
			run = resp.GetItems()[0]
		default:
			return nil, fmt.Errorf("panel key %s matched more than one row", id)
		}
	}
	if run.GetTenantNamespace() != tenant {
		return nil, ErrPanelRunNotFound
	}
	return run, nil
}
