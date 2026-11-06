package daemon

import (
	"context"
	"errors"
	"strings"
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
	// Records is the record plane stratum serves over ContextDocument,
	// Session and PanelRun: its write surface and its reads. main wires it.
	Records interface {
		contextRecords
		sessionRecords
		panelRecords
	}
	// NewProvider builds the model provider for a model alias, and NewPanelist
	// a panel seat's; main wires the llmproxy client into both, tests set
	// Engine and Panel directly.
	NewProvider func(modelAlias string) Provider
	NewPanelist func(modelAlias string) Panelist

	mu       sync.RWMutex
	Sessions *SessionStore
	Engine   *Engine
	Panels   *PanelStore
	// Panel is nil when no panel_model_aliases are configured.
	Panel *Panel
}

var _ sm.Handler = (*Server)(nil)

// DoppelgangerGrpcImpl returns the handler the generated wrapper serves.
func (s *Server) DoppelgangerGrpcImpl() fe.DoppelgangerHandler { return s }

// DoppelgangerConfig is the generated service manager's config delivery, at
// start and on every change: model_alias, panel_model_aliases,
// panel_moderator_alias and principles_paths take effect here. An answer in
// flight finishes with the engine (or panel) it started with.
func (s *Server) DoppelgangerConfig(cfg *pb.Config) error {
	if s.NewProvider == nil {
		return errors.New("doppelganger: no provider constructor wired")
	}
	if len(cfg.GetPanelModelAliases()) > 0 && s.NewPanelist == nil {
		return errors.New("doppelganger: panel_model_aliases set but no panelist constructor wired")
	}
	if s.Records == nil {
		return errors.New("doppelganger: no record plane wired")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Sessions == nil {
		s.Sessions = NewSessionStore(s.Records)
	}
	if s.Panels == nil {
		s.Panels = NewPanelStore(s.Records)
	}
	contexts := NewContextStore(s.Records)
	s.Engine = &Engine{Contexts: contexts, Provider: s.NewProvider(cfg.GetModelAlias())}
	s.Panel = nil
	if aliases := cfg.GetPanelModelAliases(); len(aliases) > 0 {
		panel := &Panel{Contexts: contexts, PrinciplesPaths: append([]string{}, cfg.GetPrinciplesPaths()...)}
		for _, alias := range aliases {
			panel.Seats = append(panel.Seats, PanelSeat{Alias: alias, Panelist: s.NewPanelist(alias)})
		}
		// The moderator is its own instance, even on a seat's alias.
		if alias := cfg.GetPanelModeratorAlias(); alias != "" {
			panel.Moderator = &PanelSeat{Alias: alias, Panelist: s.NewPanelist(alias)}
		}
		s.Panel = panel
	}
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

// currentPanel is the panel (nil when none is configured) and the run store
// the next panel RPC uses.
func (s *Server) currentPanel() (*Panel, *PanelStore) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Panel, s.Panels
}

// Shutdown runs after the listener has drained. Sessions are written on every
// change, so there is nothing to flush.
func (s *Server) Shutdown(context.Context) {}

// callerIdentity is who an RPC acts for: the token's principal (its subject
// when no principal is set) and the tenant namespace the token carries. Every
// RPC here is a user's, so no caller is a refusal, not an anonymous session.
func callerIdentity(caller *servicemanager.Caller) (user, tenant string, err error) {
	if caller == nil || caller.Token() == nil {
		return "", "", status.Error(codes.Unauthenticated, "no caller identity")
	}
	user = caller.Token().GetPrincipal()
	if user == "" {
		user = caller.Token().GetSubject()
	}
	if user == "" {
		return "", "", status.Error(codes.Unauthenticated, "caller token names no principal")
	}
	tenant = caller.Token().GetNamespace()
	if tenant == "" {
		return "", "", status.Error(codes.Unauthenticated, "caller token names no tenant")
	}
	return user, tenant, nil
}

func (s *Server) CreateSession(ctx context.Context, caller *servicemanager.Caller, req *pb.CreateSessionRequest) (*pb.CreateSessionResponse, error) {
	ctx = withTraceID(ctx, req.GetTraceId())
	ctx = withTargetTenant(ctx, req.GetTenant())
	user, tenant, err := callerIdentity(caller)
	if err != nil {
		return nil, err
	}
	sessions, _ := s.current()
	sess, err := sessions.Create(ctx, user, tenant, req.GetContext())
	if err != nil {
		return nil, storeError(err, codes.Internal)
	}
	return &pb.CreateSessionResponse{SessionId: sess.ID}, nil
}

func (s *Server) UpdateSession(ctx context.Context, caller *servicemanager.Caller, req *pb.UpdateSessionRequest) (*pb.UpdateSessionResponse, error) {
	ctx = withTraceID(ctx, req.GetTraceId())
	ctx = withTargetTenant(ctx, req.GetTenant())
	user, _, err := callerIdentity(caller)
	if err != nil {
		return nil, err
	}
	sessions, _ := s.current()
	if err := sessions.AppendContext(ctx, req.GetSessionId(), user, req.GetContext()); err != nil {
		return nil, sessionError(err)
	}
	return &pb.UpdateSessionResponse{}, nil
}

// UpdateContext replaces the caller's own context; it can reach nobody else's.
func (s *Server) UpdateContext(ctx context.Context, caller *servicemanager.Caller, req *pb.UpdateContextRequest) (*pb.UpdateContextResponse, error) {
	ctx = withTraceID(ctx, req.GetTraceId())
	ctx = withTargetTenant(ctx, req.GetTenant())
	user, tenant, err := callerIdentity(caller)
	if err != nil {
		return nil, err
	}
	_, engine := s.current()
	n, bytes, err := engine.Contexts.Replace(ctx, tenant, scopeUser, user, req.GetFiles())
	if err != nil {
		return nil, contextError(err)
	}
	return &pb.UpdateContextResponse{FileCount: int32(n), ByteCount: bytes}, nil
}

// UpdateTenantContext replaces the tenant's shared context. Who may call it is
// the generated authorizer's decision (this RPC's grant); here it is only done.
func (s *Server) UpdateTenantContext(ctx context.Context, caller *servicemanager.Caller, req *pb.UpdateTenantContextRequest) (*pb.UpdateTenantContextResponse, error) {
	ctx = withTraceID(ctx, req.GetTraceId())
	ctx = withTargetTenant(ctx, req.GetTenant())
	_, tenant, err := callerIdentity(caller)
	if err != nil {
		return nil, err
	}
	_, engine := s.current()
	n, bytes, err := engine.Contexts.Replace(ctx, tenant, scopeTenant, tenant, req.GetFiles())
	if err != nil {
		return nil, contextError(err)
	}
	return &pb.UpdateTenantContextResponse{FileCount: int32(n), ByteCount: bytes}, nil
}

func contextError(err error) error {
	if errors.Is(err, errBadContextPath) || errors.Is(err, errDuplicateContextPath) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return storeError(err, codes.Unavailable)
}

func (s *Server) GenerateAnswer(ctx context.Context, caller *servicemanager.Caller, req *pb.GenerateAnswerRequest) (*pb.GenerateAnswerResponse, error) {
	ctx = withTraceID(ctx, req.GetTraceId())
	ctx = withTargetTenant(ctx, req.GetTenant())
	user, _, err := callerIdentity(caller)
	if err != nil {
		return nil, err
	}
	sessions, engine := s.current()
	sess, err := sessions.Get(ctx, req.GetSessionId(), user)
	if err != nil {
		return nil, sessionError(err)
	}
	a, err := engine.Answer(ctx, sess, req.GetQuestion())
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	if err := sessions.AppendTurn(ctx, sess.ID, user, Turn{Question: req.GetQuestion(), Answer: a}); err != nil {
		return nil, storeError(err, codes.Internal)
	}
	return answerToProto(a), nil
}

func sessionError(err error) error {
	if errors.Is(err, ErrSessionNotFound) {
		return status.Error(codes.NotFound, err.Error())
	}
	return storeError(err, codes.Unavailable)
}

// answerToProto and answerFromProto are the two halves of one mapping: the
// engine's Answer is what the model produced, GenerateAnswerResponse is what
// the caller receives and what a session row remembers.
func answerToProto(a Answer) *pb.GenerateAnswerResponse {
	return &pb.GenerateAnswerResponse{
		Kind:            pb.AnswerKind(pb.AnswerKind_value["ANSWER_KIND_"+a.Kind]),
		Answer:          a.Answer,
		Reasoning:       a.Reasoning,
		WouldChangeMind: a.WouldChangeMind,
		Provenance:      pb.Provenance(pb.Provenance_value["PROVENANCE_"+a.Provenance]),
		Sources:         append([]string{}, a.Sources...),
		Missing:         append([]string{}, a.Missing...),
	}
}

func answerFromProto(r *pb.GenerateAnswerResponse) Answer {
	return Answer{
		Kind:            strings.TrimPrefix(r.GetKind().String(), "ANSWER_KIND_"),
		Answer:          r.GetAnswer(),
		Reasoning:       r.GetReasoning(),
		WouldChangeMind: r.GetWouldChangeMind(),
		Provenance:      strings.TrimPrefix(r.GetProvenance().String(), "PROVENANCE_"),
		Sources:         append([]string{}, r.GetSources()...),
		Missing:         append([]string{}, r.GetMissing()...),
	}
}

// GeneratePanelAnswer asks the panel and keeps the run. No panel, or no
// principles to give it, is FailedPrecondition: the tenant's configuration or
// context must change before it can answer.
func (s *Server) GeneratePanelAnswer(ctx context.Context, caller *servicemanager.Caller, req *pb.GeneratePanelAnswerRequest) (*pb.GeneratePanelAnswerResponse, error) {
	ctx = withTraceID(ctx, req.GetTraceId())
	ctx = withTargetTenant(ctx, req.GetTenant())
	user, tenant, err := callerIdentity(caller)
	if err != nil {
		return nil, err
	}
	panel, runs := s.currentPanel()
	if panel == nil {
		return nil, status.Error(codes.FailedPrecondition, errNoPanel.Error())
	}
	run, err := panel.Run(ctx, tenant, PanelQuestion{
		Question:                  req.GetQuestion(),
		Situation:                 req.GetSituation(),
		Person:                    req.GetPerson(),
		Kind:                      req.GetQuestionKind(),
		Reversible:                req.GetReversible(),
		TouchesSecurityOrIdentity: req.GetTouchesSecurityOrIdentity(),
	})
	if errors.Is(err, errNoPanel) || errors.Is(err, errNoPrinciples) {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	if err := runs.Create(ctx, user, tenant, run); err != nil {
		return nil, storeError(err, codes.Internal)
	}
	return &pb.GeneratePanelAnswerResponse{
		PanelId:  run.GetPanelId(),
		Options:  run.GetOptions(),
		Answers:  run.GetAnswers(),
		Failures: run.GetFailures(),
		Result:   run.GetResult(),

		Moderator:          run.GetModerator(),
		FramedQuestion:     run.GetFramedQuestion(),
		FramedRequirements: run.GetFramedRequirements(),
		FramedSituation:    run.GetFramedSituation(),
		ModeratorMerged:    run.GetModeratorMerged(),
	}, nil
}

// UpdatePanelDecision records the person's choice on a run of the tenant. Who
// may call it is the generated authorizer's decision (this RPC's grant).
func (s *Server) UpdatePanelDecision(ctx context.Context, caller *servicemanager.Caller, req *pb.UpdatePanelDecisionRequest) (*pb.UpdatePanelDecisionResponse, error) {
	ctx = withTraceID(ctx, req.GetTraceId())
	ctx = withTargetTenant(ctx, req.GetTenant())
	_, tenant, err := callerIdentity(caller)
	if err != nil {
		return nil, err
	}
	_, runs := s.currentPanel()
	switch err := runs.Decide(ctx, tenant, req.GetPanelId(), req.GetOptionId()); {
	case errors.Is(err, ErrPanelRunNotFound):
		return nil, status.Error(codes.NotFound, err.Error())
	case errors.Is(err, errUnknownOption):
		return nil, status.Error(codes.InvalidArgument, err.Error())
	case err != nil:
		return nil, storeError(err, codes.Unavailable)
	}
	return &pb.UpdatePanelDecisionResponse{}, nil
}

// GetPanelTrackRecord scores the tenant's decided runs per model and question kind.
func (s *Server) GetPanelTrackRecord(ctx context.Context, caller *servicemanager.Caller, req *pb.GetPanelTrackRecordRequest) (*pb.GetPanelTrackRecordResponse, error) {
	ctx = withTraceID(ctx, req.GetTraceId())
	ctx = withTargetTenant(ctx, req.GetTenant())
	_, tenant, err := callerIdentity(caller)
	if err != nil {
		return nil, err
	}
	_, runs := s.currentPanel()
	all, err := runs.List(ctx, tenant)
	if err != nil {
		return nil, storeError(err, codes.Unavailable)
	}
	return panelTrackRecord(all), nil
}
