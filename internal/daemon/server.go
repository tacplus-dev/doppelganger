package daemon

import (
	"context"
	"errors"

	"gitlab.com/seabeam/tools/doppelganger/model/chassis/servicemanager"
	sm "gitlab.com/seabeam/tools/doppelganger/model/s2sm"
	fe "gitlab.com/seabeam/tools/doppelganger/model/s2sm/grpc/servers/services/doppelganger"
	pb "gitlab.com/seabeam/tools/doppelganger/model/types/services/doppelganger"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements the tacplus-generated Doppelganger C2D handler. The
// generated standalone host (sm.ServeC2DStandalone) wraps it with request and
// response validation and serves it on the Unix socket; C2D has no caller
// token, so caller is always nil.
type Server struct {
	Sessions *SessionStore
	Engine   *Engine
}

var _ sm.Handler = (*Server)(nil)

// DoppelgangerGrpcImpl returns the handler the generated wrapper serves.
func (s *Server) DoppelgangerGrpcImpl() fe.DoppelgangerHandler { return s }

// Shutdown runs after the socket has drained. Sessions are written on every
// change, so there is nothing to flush.
func (s *Server) Shutdown(context.Context) {}

func (s *Server) CreateSession(_ context.Context, _ *servicemanager.Caller, req *pb.CreateSessionRequest) (*pb.CreateSessionResponse, error) {
	sess, err := s.Sessions.Create(req.GetContext())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.CreateSessionResponse{SessionId: sess.ID}, nil
}

func (s *Server) UpdateSession(_ context.Context, _ *servicemanager.Caller, req *pb.UpdateSessionRequest) (*pb.UpdateSessionResponse, error) {
	if err := s.Sessions.AppendContext(req.GetSessionId(), req.GetContext()); err != nil {
		return nil, sessionError(err)
	}
	return &pb.UpdateSessionResponse{}, nil
}

func (s *Server) GenerateAnswer(ctx context.Context, _ *servicemanager.Caller, req *pb.GenerateAnswerRequest) (*pb.GenerateAnswerResponse, error) {
	sess, err := s.Sessions.Get(req.GetSessionId())
	if err != nil {
		return nil, sessionError(err)
	}
	a, err := s.Engine.Answer(ctx, sess, req.GetQuestion())
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	if err := s.Sessions.AppendTurn(sess.ID, Turn{Question: req.GetQuestion(), Answer: a}); err != nil {
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
