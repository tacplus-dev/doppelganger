// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	query_pb "github.com/tacplus-dev/doppelganger/tacplus/types/vservices/query"
)

// Session is one caller conversation as the engine sees it: the context they
// gave and the turns so far. It is stored as the Session record in stratum.
type Session struct {
	ID string
	// Owner is the user who opened the session; nobody else can read or extend it.
	Owner string
	// Tenant is the namespace the session's context is read from.
	Tenant    string
	CreatedAt time.Time
	Context   []string
	Turns     []Turn
}

// Turn is one question and the answer given. Earlier turns are replayed into
// every later question so that pushback is answered, not repeated.
type Turn struct {
	Question string
	Answer   Answer
}

var ErrSessionNotFound = errors.New("session not found")

var sessionIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// sessionRecords is the generated record plane's S2S half over Session:
// stratum's write surface and its point read.
type sessionRecords interface {
	GetSession(ctx context.Context, req *query_pb.GetSessionRequest, opts ...grpc.CallOption) (*query_pb.GetSessionResponse, error)
	InsertIfAbsentSession(ctx context.Context, req *query_pb.InsertIfAbsentSessionRequest, opts ...grpc.CallOption) (*query_pb.RecordWriteResult, error)
	ReplaceIfUnchangedSession(ctx context.Context, req *query_pb.ReplaceIfUnchangedSessionRequest, opts ...grpc.CallOption) (*query_pb.RecordWriteResult, error)
}

// SessionStore keeps sessions as Session records in stratum: a new session is
// inserted, every change rewrites the whole session on the revision it read
// (read again and redone when another write got there first), and a read is
// Get on the key.
type SessionStore struct {
	records sessionRecords
	now     func() time.Time
}

func NewSessionStore(records sessionRecords) *SessionStore {
	return &SessionStore{records: records, now: time.Now}
}

func (s *SessionStore) Create(ctx context.Context, owner, tenant string, context_ []string) (*Session, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	sess := &Session{
		ID:        hex.EncodeToString(buf),
		Owner:     owner,
		Tenant:    tenant,
		CreatedAt: s.now().UTC(),
		Context:   append([]string{}, context_...),
		Turns:     []Turn{},
	}
	_, err := s.records.InsertIfAbsentSession(ctx, &query_pb.InsertIfAbsentSessionRequest{
		Item:         s.record(ctx, sess),
		TargetTenant: targetTenant(ctx),
		TraceId:      traceID(ctx),
	})
	if err != nil {
		// ALREADY_EXISTS here is a 128-bit random id drawn twice: not retried.
		return nil, fmt.Errorf("inserting session %s: %w", sess.ID, err)
	}
	return sess, nil
}

// Get returns owner's session id. Another user's session is not found: its
// existence is theirs to know.
func (s *SessionStore) Get(ctx context.Context, id, owner string) (*Session, error) {
	rec, _, err := s.read(ctx, id, owner)
	if err != nil {
		return nil, err
	}
	return fromRecord(rec), nil
}

func (s *SessionStore) AppendContext(ctx context.Context, id, owner string, context_ []string) error {
	return s.update(ctx, id, owner, func(sess *Session) { sess.Context = append(sess.Context, context_...) })
}

func (s *SessionStore) AppendTurn(ctx context.Context, id, owner string, turn Turn) error {
	return s.update(ctx, id, owner, func(sess *Session) { sess.Turns = append(sess.Turns, turn) })
}

// update applies change to owner's session id and rewrites it if the row is
// still the revision it read; a concurrent write (ABORTED) is read and redone,
// up to maxWriteAttempts.
func (s *SessionStore) update(ctx context.Context, id, owner string, change func(*Session)) error {
	for attempt := 1; ; attempt++ {
		rec, revision, err := s.read(ctx, id, owner)
		if err != nil {
			return err
		}
		sess := fromRecord(rec)
		change(sess)
		_, err = s.records.ReplaceIfUnchangedSession(ctx, &query_pb.ReplaceIfUnchangedSessionRequest{
			Item:         s.record(ctx, sess),
			Expected:     &query_pb.ReplaceIfUnchangedSessionRequest_ExpectedRevision{ExpectedRevision: revision},
			TargetTenant: targetTenant(ctx),
			TraceId:      traceID(ctx),
		})
		switch {
		case err == nil:
			return nil
		case status.Code(err) == codes.Aborted && attempt < maxWriteAttempts:
			continue
		case status.Code(err) == codes.NotFound:
			return ErrSessionNotFound
		default:
			return fmt.Errorf("writing session %s: %w", id, err)
		}
	}
}

// record is sess as its row, stamped now and with the trace_id of the
// request writing it.
func (s *SessionStore) record(ctx context.Context, sess *Session) *pb.Session {
	rec := toRecord(sess, s.now().UnixMilli())
	rec.TraceId = traceID(ctx)
	return rec
}

// read is owner's session id and the revision it is at; a malformed id, a
// row that is not there or another user's is ErrSessionNotFound.
func (s *SessionStore) read(ctx context.Context, id, owner string) (*pb.Session, int64, error) {
	if !sessionIDPattern.MatchString(id) {
		return nil, 0, ErrSessionNotFound
	}
	resp, err := s.records.GetSession(ctx, &query_pb.GetSessionRequest{
		SessionId:    id,
		TargetTenant: targetTenant(ctx),
		TraceId:      traceID(ctx),
	})
	if status.Code(err) == codes.NotFound {
		return nil, 0, ErrSessionNotFound
	}
	if err != nil {
		return nil, 0, fmt.Errorf("reading session %s: %w", id, err)
	}
	rec := resp.GetItem()
	if rec == nil || rec.GetDeleted() || rec.GetOwner() != owner {
		return nil, 0, ErrSessionNotFound
	}
	return rec, resp.GetRevision(), nil
}

func toRecord(sess *Session, nowMs int64) *pb.Session {
	rec := &pb.Session{
		SessionId:       sess.ID,
		TenantNamespace: sess.Tenant,
		Owner:           sess.Owner,
		Context:         append([]string{}, sess.Context...),
		CreatedAtMs:     sess.CreatedAt.UnixMilli(),
		UpdatedAtMs:     nowMs,
	}
	for _, t := range sess.Turns {
		rec.Turns = append(rec.Turns, &pb.SessionTurn{Question: t.Question, Answer: answerToProto(t.Answer)})
	}
	return rec
}

func fromRecord(rec *pb.Session) *Session {
	sess := &Session{
		ID:        rec.GetSessionId(),
		Owner:     rec.GetOwner(),
		Tenant:    rec.GetTenantNamespace(),
		CreatedAt: time.UnixMilli(rec.GetCreatedAtMs()).UTC(),
		Context:   append([]string{}, rec.GetContext()...),
		Turns:     []Turn{},
	}
	for _, t := range rec.GetTurns() {
		sess.Turns = append(sess.Turns, Turn{Question: t.GetQuestion(), Answer: answerFromProto(t.GetAnswer())})
	}
	return sess
}
