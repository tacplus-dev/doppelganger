package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// Session is the durable state of one caller conversation. One JSON file per
// session in the sessions directory. Stale-session cleanup is not implemented yet.
type Session struct {
	ID string `json:"id"`
	// Owner is the user who opened the session; nobody else can read or extend it.
	Owner     string    `json:"owner"`
	CreatedAt time.Time `json:"created_at"`
	Context   []string  `json:"context"`
	Turns     []Turn    `json:"turns"`
}

// Turn is one question and the answer given. Earlier turns are replayed into
// every later question so that pushback is answered, not repeated.
type Turn struct {
	Question string `json:"question"`
	Answer   Answer `json:"answer"`
}

var ErrSessionNotFound = errors.New("session not found")

var sessionIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type SessionStore struct {
	dir string
	mu  sync.Mutex
}

func NewSessionStore(dir string) (*SessionStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create sessions dir: %w", err)
	}
	return &SessionStore{dir: dir}, nil
}

func (s *SessionStore) Create(owner string, context []string) (*Session, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	sess := &Session{
		ID:        hex.EncodeToString(buf),
		Owner:     owner,
		CreatedAt: time.Now().UTC(),
		Context:   append([]string{}, context...),
		Turns:     []Turn{},
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return sess, s.write(sess)
}

// Get returns owner's session id. Another user's session is not found: its
// existence is theirs to know.
func (s *SessionStore) Get(id, owner string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readOwned(id, owner)
}

func (s *SessionStore) AppendContext(id, owner string, context []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.readOwned(id, owner)
	if err != nil {
		return err
	}
	sess.Context = append(sess.Context, context...)
	return s.write(sess)
}

func (s *SessionStore) AppendTurn(id, owner string, turn Turn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.readOwned(id, owner)
	if err != nil {
		return err
	}
	sess.Turns = append(sess.Turns, turn)
	return s.write(sess)
}

func (s *SessionStore) path(id string) (string, error) {
	// The id becomes a filename, so it must be exactly what Create mints.
	if !sessionIDPattern.MatchString(id) {
		return "", ErrSessionNotFound
	}
	return filepath.Join(s.dir, id+".json"), nil
}

func (s *SessionStore) readOwned(id, owner string) (*Session, error) {
	sess, err := s.read(id)
	if err != nil {
		return nil, err
	}
	if sess.Owner != owner {
		return nil, ErrSessionNotFound
	}
	return sess, nil
}

func (s *SessionStore) read(id string) (*Session, error) {
	p, err := s.path(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("decode session %s: %w", id, err)
	}
	return &sess, nil
}

// write replaces the session file atomically (temp file + rename).
func (s *SessionStore) write(sess *Session) error {
	p, err := s.path(sess.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, sess.ID+".*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p)
}
