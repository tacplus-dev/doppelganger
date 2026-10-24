// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"regexp"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

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

// sessionRecords is the generated record query plane's S2S half over Session.
type sessionRecords interface {
	CreateSessionQuery(ctx context.Context, req *query_pb.OpenSessionQueryRequest, opts ...grpc.CallOption) (*query_pb.QueryHandle, error)
	ListSession(ctx context.Context, req *query_pb.PullSessionRequest, opts ...grpc.CallOption) iter.Seq2[*pb.Session, error]
	DeleteSessionQuery(ctx context.Context, req *query_pb.QueryHandle, opts ...grpc.CallOption) (*query_pb.QueryStats, error)
}

// recentTTL bounds the write-through cache below: a session this instance
// published is served from memory this long, which covers the moment between
// publishing a row and stratum having stored it (a caller opens a session and
// asks in the next call). After that the store is the only truth.
const recentTTL = 10 * time.Minute

type recentSession struct {
	sess    *pb.Session
	expires time.Time
}

// SessionStore keeps sessions as Session records in stratum: every change
// republishes the whole session; reads are a point query on the key, after
// the write-through cache of this instance's own recent publishes.
type SessionStore struct {
	records sessionRecords
	publish func(context.Context, *pb.Session) error
	now     func() time.Time

	mu     sync.Mutex
	recent map[string]recentSession
}

func NewSessionStore(records sessionRecords, publish func(context.Context, *pb.Session) error) *SessionStore {
	return &SessionStore{records: records, publish: publish, now: time.Now, recent: map[string]recentSession{}}
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
	return sess, s.write(ctx, sess)
}

// Get returns owner's session id. Another user's session is not found: its
// existence is theirs to know.
func (s *SessionStore) Get(ctx context.Context, id, owner string) (*Session, error) {
	rec, err := s.read(ctx, id)
	if err != nil {
		return nil, err
	}
	if rec.GetOwner() != owner {
		return nil, ErrSessionNotFound
	}
	return fromRecord(rec), nil
}

func (s *SessionStore) AppendContext(ctx context.Context, id, owner string, context_ []string) error {
	sess, err := s.Get(ctx, id, owner)
	if err != nil {
		return err
	}
	sess.Context = append(sess.Context, context_...)
	return s.write(ctx, sess)
}

func (s *SessionStore) AppendTurn(ctx context.Context, id, owner string, turn Turn) error {
	sess, err := s.Get(ctx, id, owner)
	if err != nil {
		return err
	}
	sess.Turns = append(sess.Turns, turn)
	return s.write(ctx, sess)
}

// write publishes the whole session and remembers it for recentTTL.
func (s *SessionStore) write(ctx context.Context, sess *Session) error {
	rec := toRecord(sess, s.now().UnixMilli())
	if err := s.publish(ctx, rec); err != nil {
		return fmt.Errorf("publishing session %s: %w", sess.ID, err)
	}
	s.mu.Lock()
	s.recent[sess.ID] = recentSession{sess: rec, expires: s.now().Add(recentTTL)}
	s.mu.Unlock()
	return nil
}

// read is this instance's recent publish of id if it has one, else the
// store's row: a point query on the key, paged (one row), and closed.
func (s *SessionStore) read(ctx context.Context, id string) (*pb.Session, error) {
	if !sessionIDPattern.MatchString(id) {
		return nil, ErrSessionNotFound
	}
	s.mu.Lock()
	now := s.now()
	for k, r := range s.recent {
		if !now.Before(r.expires) {
			delete(s.recent, k)
		}
	}
	if r, ok := s.recent[id]; ok {
		s.mu.Unlock()
		return proto.Clone(r.sess).(*pb.Session), nil
	}
	s.mu.Unlock()

	handle, err := s.records.CreateSessionQuery(ctx, &query_pb.OpenSessionQueryRequest{
		Filter: &query_pb.SessionFilter{SessionId: &id},
	})
	if err != nil {
		return nil, fmt.Errorf("opening Session query: %w", err)
	}
	defer s.records.DeleteSessionQuery(ctx, &query_pb.QueryHandle{QueryId: handle.GetQueryId()}) //nolint:errcheck
	var found *pb.Session
	for rec, err := range s.records.ListSession(ctx, &query_pb.PullSessionRequest{QueryId: handle.GetQueryId(), PageSize: 2}) {
		if err != nil {
			return nil, fmt.Errorf("reading Session row: %w", err)
		}
		if found != nil {
			return nil, fmt.Errorf("session key %s matched more than one row", id)
		}
		found = rec
	}
	if found == nil {
		return nil, ErrSessionNotFound
	}
	return found, nil
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
