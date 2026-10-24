package daemon

import (
	"context"
	"errors"
	"sync"

	"github.com/tacplus-dev/doppelganger/tacplus/chassis/servicemanager"
	sm "github.com/tacplus-dev/doppelganger/tacplus/s2sm"
	fe "github.com/tacplus-dev/doppelganger/tacplus/s2sm/grpc/servers/services/doppelganger"
	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements the tacplus-generated Doppelganger handler. The generated
// service manager wraps it with authentication, request and response
// validation and serves it on the fleet's gRPC surface; the caller is the
// authenticated user the gateway admitted, not used yet (every RPC is
// RPC_FLAG_AUTH_ANY_USER).
type Server struct {
	// NewProvider builds the model provider for a model alias; main wires the
	// llmproxy client in, tests set Engine directly.
	NewProvider func(modelAlias string) Provider

	mu       sync.RWMutex
	Sessions *SessionStore
	Engine   *Engine
}

var _ sm.Handler = (*Server)(nil)

// DoppelgangerGrpcImpl returns the handler the generated wrapper serves.
func (s *Server) DoppelgangerGrpcImpl() fe.DoppelgangerHandler { return s }

// DoppelgangerConfig is the generated service manager's config delivery, at
// start and on every change: state_dir, repo_dir and model_alias take effect
// here. An answer in flight finishes with the engine it started with.
func (s *Server) DoppelgangerConfig(cfg *pb.Config) error {
	sessions, err := NewSessionStore(cfg.GetStateDir())
	if err != nil {
		return err
	}
	if s.NewProvider == nil {
		return errors.New("doppelganger: no provider constructor wired")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Sessions = sessions
	s.Engine = &Engine{RepoDir: cfg.GetRepoDir(), Provider: s.NewProvider(cfg.GetModelAlias())}
	return nil
}

// DoppelgangerConfigStateConfig is the generated config hook for the appended
// DoppelgangerConfigStateService (tacplus emits one per gRPC service in the
// package); it carries the same Config DoppelgangerConfig already applied.
func (s *Server) DoppelgangerConfigStateConfig(*pb.Config) error { return nil }

// DoppelgangerConfigStateGrpcImpl is the generated per-service config state
// surface. It is served by the config store (kernel), not by this service, so
// it is refused here — the same shape every Go fleet service carries today.
func (s *Server) DoppelgangerConfigStateGrpcImpl() fe.DoppelgangerConfigStateServiceHandler {
	return configStateNotHosted{}
}

type configStateNotHosted struct{}

func (configStateNotHosted) GetConfig(context.Context, *servicemanager.Caller) (*pb.Config, error) {
	return nil, errConfigStateNotHosted
}

func (configStateNotHosted) SetConfig(context.Context, *servicemanager.Caller, *pb.Config) (*pb.Config, error) {
	return nil, errConfigStateNotHosted
}

func (configStateNotHosted) WatchConfig(*servicemanager.Caller, grpc.ServerStreamingServer[pb.Config]) error {
	return errConfigStateNotHosted
}

var errConfigStateNotHosted = status.Error(codes.Unimplemented,
	"doppelganger's config state is served by the config store, not by doppelganger")

// current is the store and engine the next RPC uses.
func (s *Server) current() (*SessionStore, *Engine) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Sessions, s.Engine
}

// Shutdown runs after the listener has drained. Sessions are written on every
// change, so there is nothing to flush.
func (s *Server) Shutdown(context.Context) {}

func (s *Server) CreateSession(_ context.Context, _ *servicemanager.Caller, req *pb.CreateSessionRequest) (*pb.CreateSessionResponse, error) {
	sessions, _ := s.current()
	sess, err := sessions.Create(req.GetContext())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.CreateSessionResponse{SessionId: sess.ID}, nil
}

func (s *Server) UpdateSession(_ context.Context, _ *servicemanager.Caller, req *pb.UpdateSessionRequest) (*pb.UpdateSessionResponse, error) {
	sessions, _ := s.current()
	if err := sessions.AppendContext(req.GetSessionId(), req.GetContext()); err != nil {
		return nil, sessionError(err)
	}
	return &pb.UpdateSessionResponse{}, nil
}

func (s *Server) GenerateAnswer(ctx context.Context, _ *servicemanager.Caller, req *pb.GenerateAnswerRequest) (*pb.GenerateAnswerResponse, error) {
	sessions, engine := s.current()
	sess, err := sessions.Get(req.GetSessionId())
	if err != nil {
		return nil, sessionError(err)
	}
	a, err := engine.Answer(ctx, sess, req.GetQuestion())
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	if err := sessions.AppendTurn(sess.ID, Turn{Question: req.GetQuestion(), Answer: a}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.GenerateAnswerResponse{
		Kind:            pb.AnswerKind(pb.AnswerKind_value["ANSWER_KIND_"+a.Kind]),
		Answer:          a.Answer,
		Reasoning:       a.Reasoning,
		WouldChangeMind: a.WouldChangeMind,
		Provenance:      pb.Provenance(pb.Provenance_value["PROVENANCE_"+a.Provenance]),
		Sources:         a.Sources,
		Missing:         a.Missing,
	}, nil
}

func sessionError(err error) error {
	if errors.Is(err, ErrSessionNotFound) {
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}
