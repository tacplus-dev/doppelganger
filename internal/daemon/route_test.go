package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/tacplus-dev/doppelganger/tacplus/chassis/grpcserver"
	"github.com/tacplus-dev/doppelganger/tacplus/chassis/token"
	fe "github.com/tacplus-dev/doppelganger/tacplus/s2sm/grpc/servers/services/doppelganger"
	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// acmeTid is the tid of tenant acme: its instance's subject names it, its
// namespace claim (/tenants/acme) names the tenant by name (kernel#23).
const acmeTid = "acme0001"

// routedInstance is acme's instance as s2sm wires it: the generated wrapper
// around s, its routing backstop the chassis authorizer's CheckRoute, which
// knows the route field of every rpc and the instance's own tid. Every call
// authenticates as jody of acme.
func routedInstance(t *testing.T, s *Server) (*fe.Doppelganger, grpcserver.Authorizer) {
	t.Helper()
	auth, err := grpcserver.NewAuthorizer(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	auth.SetHostIdentity(acme, "spiffe://example.com/tenants/"+acmeTid+"/services/doppelganger")
	fe.RegisterDoppelgangerMethodAuth(auth)
	w := fe.NewDoppelganger(s)
	w.SetAuthenticateFunc(func(context.Context) (*token.TokenWrapper, error) { return asUser("jody").Token(), nil })
	w.SetRouteCheckFunc(auth.CheckRoute)
	return w, auth
}

// TestRouteFieldReachesNamedTenant: a call whose tenant is acme's tid reaches
// acme's instance and its handler; one with no tid, a malformed tid or
// another tenant's tid is refused before the handler, so nothing is stored.
func TestRouteFieldReachesNamedTenant(t *testing.T) {
	rec := newFakeRecords()
	store := NewSessionStore(rec, rec.publishSession)
	w, _ := routedInstance(t, &Server{Sessions: store})
	ctx := context.Background()

	created, err := w.CreateSession(ctx, &pb.CreateSessionRequest{Tenant: acmeTid, Context: []string{"audience: Jody"}})
	if err != nil {
		t.Fatalf("call routed to acme's tid: %v", err)
	}
	sess, err := store.Get(ctx, created.GetSessionId(), "jody")
	if err != nil || sess.Tenant != acme {
		t.Fatalf("session not created in acme: %v %v", sess, err)
	}

	for _, c := range []struct {
		tenant string
		code   codes.Code
	}{
		{"", codes.InvalidArgument},
		{"ACME0001", codes.InvalidArgument},
		{"acme", codes.InvalidArgument},
		{"/tenants/acme", codes.InvalidArgument},
		{"other001", codes.PermissionDenied},
	} {
		before := len(rec.sessions)
		_, err := w.CreateSession(ctx, &pb.CreateSessionRequest{Tenant: c.tenant})
		if status.Code(err) != c.code {
			t.Fatalf("tenant %q: err = %v, want %v", c.tenant, err, c.code)
		}
		if len(rec.sessions) != before {
			t.Fatalf("tenant %q: refused call reached the handler", c.tenant)
		}
	}
}

// TestEveryRpcRoutesByTenant: each rpc of the service routes on its request's
// tenant field: acme's tid passes acme's instance's backstop, a missing tid
// is refused, another tenant's is refused.
func TestEveryRpcRoutesByTenant(t *testing.T) {
	_, auth := routedInstance(t, &Server{})
	rpcs := map[string]func(tenant string) proto.Message{
		pb.Doppelganger_CreateSession_FullMethodName:       func(tid string) proto.Message { return &pb.CreateSessionRequest{Tenant: tid} },
		pb.Doppelganger_UpdateSession_FullMethodName:       func(tid string) proto.Message { return &pb.UpdateSessionRequest{Tenant: tid} },
		pb.Doppelganger_GenerateAnswer_FullMethodName:      func(tid string) proto.Message { return &pb.GenerateAnswerRequest{Tenant: tid} },
		pb.Doppelganger_UpdateContext_FullMethodName:       func(tid string) proto.Message { return &pb.UpdateContextRequest{Tenant: tid} },
		pb.Doppelganger_UpdateTenantContext_FullMethodName: func(tid string) proto.Message { return &pb.UpdateTenantContextRequest{Tenant: tid} },
		pb.Doppelganger_GeneratePanelAnswer_FullMethodName: func(tid string) proto.Message { return &pb.GeneratePanelAnswerRequest{Tenant: tid} },
		pb.Doppelganger_UpdatePanelDecision_FullMethodName: func(tid string) proto.Message { return &pb.UpdatePanelDecisionRequest{Tenant: tid} },
		pb.Doppelganger_GetPanelTrackRecord_FullMethodName: func(tid string) proto.Message { return &pb.GetPanelTrackRecordRequest{Tenant: tid} },
	}
	if n := pb.File_services_doppelganger_proto.Services().ByName("Doppelganger").Methods().Len(); n != len(rpcs)+1 {
		t.Fatalf("Doppelganger has %d rpcs, this test covers %d and the MCP session: cover the new one", n, len(rpcs))
	}
	for method, req := range rpcs {
		full := "/services" + method
		if err := auth.CheckRoute(full, req(acmeTid)); err != nil {
			t.Errorf("%s routed to acme: %v", method, err)
		}
		err := auth.CheckRoute(full, req(""))
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), "route field tenant is empty") {
			t.Errorf("%s with no tid: err = %v, want the empty route field refused", method, err)
		}
		if err := auth.CheckRoute(full, req("other001")); status.Code(err) != codes.PermissionDenied {
			t.Errorf("%s routed to another tenant: err = %v, want PermissionDenied", method, err)
		}
	}
}
