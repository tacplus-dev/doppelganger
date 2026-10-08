package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	pb "gitlab.com/seabeam/tools/doppelganger/model/types/services/doppelganger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements the tacplus-generated Doppelganger C2D service.
type Server struct {
	pb.UnimplementedDoppelgangerServer
	Sessions *SessionStore
	Engine   *Engine
}

func (s *Server) CreateSession(_ context.Context, req *pb.CreateSessionRequest) (*pb.CreateSessionResponse, error) {
	sess, err := s.Sessions.Create(req.GetContext())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.CreateSessionResponse{SessionId: sess.ID}, nil
}

func (s *Server) UpdateSession(_ context.Context, req *pb.UpdateSessionRequest) (*pb.UpdateSessionResponse, error) {
	if err := s.Sessions.AppendContext(req.GetSessionId(), req.GetContext()); err != nil {
		return nil, sessionError(err)
	}
	return &pb.UpdateSessionResponse{}, nil
}

func (s *Server) GenerateAnswer(ctx context.Context, req *pb.GenerateAnswerRequest) (*pb.GenerateAnswerResponse, error) {
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

// Serve listens on the Unix socket until ctx is cancelled. Tacplus generates a
// C2D server wrapper for Rust only; for Go it emits the gRPC registration, so the
// listener is wired here. The socket is 0600: filesystem permissions are the
// C2D security boundary.
func Serve(ctx context.Context, socketPath string, srv *Server) error {
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		lis.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}
	gs := grpc.NewServer()
	pb.RegisterDoppelgangerServer(gs, srv)
	go func() {
		<-ctx.Done()
		gs.GracefulStop()
	}()
	return gs.Serve(lis)
}
