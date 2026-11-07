package partner

import (
	"github.com/brickKit/be-sdk-go/v3/grpcx"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/runtime"
	partnerv3 "github.com/brickKit/mdm-partner/contract/v3/gen/go/be/mdm/partner/v3"
	core "github.com/brickKit/mdm-partner/v3/internal/core"
	partners "github.com/brickKit/mdm-partner/v3/internal/partners"
	roles "github.com/brickKit/mdm-partner/v3/internal/roles"
)

// Module is the partner component as the launcher and the shells run it: it only assembles the business modules
// under internal/ (their map is internal/AGENTS.md); the gRPC services answer through them (services.go).
type Module struct {
	partnerv3.UnimplementedSourceServiceServer
	partnerv3.UnimplementedPartnerServiceServer
	b *core.Base
}

// New builds the module from its runtime.
func New(rt *runtime.Runtime) (*Module, error) {
	return &Module{b: &core.Base{RT: rt, Deployment: rt.Config.String("DEPLOYMENT_ID"),
		Instance: rt.Config.String("IMPLEMENTATION_INSTANCE")}}, nil
}

// Routes registers the user surface (contract/openapi.yaml).
func (m *Module) Routes(r *httpx.Routes) error {
	add := func(method, path, op string, h httpx.Handler) {
		r.Add(httpx.Route{Method: method, Path: path, Operation: op, Handle: h})
	}
	h := m.handler
	add("GET", "/api/partner/partners", core.OpRead, h(partners.ListPartners))
	add("POST", "/api/partner/partners", core.OpCreate, h(partners.CreatePartner))
	add("GET", "/api/partner/partners/:id", core.OpRead, h(partners.GetPartner))
	add("PATCH", "/api/partner/partners/:id", core.OpEdit, h(partners.UpdatePartner))
	add("POST", "/api/partner/partners/:id/state", core.OpEdit, h(partners.PartnerState))
	add("GET", "/api/partner/partners/:id/roles", core.OpRead, h(roles.ListRoles))
	add("PUT", "/api/partner/partners/:id/roles/customer/:legal_entity_id", core.OpRole, h(roles.PutCustomerRole))
	add("POST", "/api/partner/partners/:id/roles/customer/:legal_entity_id/state", core.OpRole, h(roles.CustomerRoleState))
	add("GET", "/api/partner/eligibility", core.OpRead, h(roles.EligibilityHTTP))
	// in the contract, not in step 6 (D75)
	for _, u := range []struct{ method, path, op string }{
		{"PUT", "/api/partner/partners/:id/roles/supplier/:legal_entity_id", core.OpRole},
		{"PUT", "/api/partner/partners/:id/roles/employee/:legal_entity_id", core.OpRole},
		{"POST", "/api/partner/partners/:id/merge", core.OpMerge},
		{"POST", "/api/partner/partners/:id/split", core.OpMerge},
		{"GET", "/api/partner/partners/:id/bank-accounts", core.OpRead},
		{"POST", "/api/partner/partners/:id/bank-accounts", core.OpEdit},
	} {
		add(u.method, u.path, u.op, core.Unsupported)
	}
	return nil
}

// Services registers the system surface: every component of the deployment resolves partners.
func (m *Module) Services(s *grpcx.Services) error {
	s.Register(&partnerv3.SourceService_ServiceDesc, m, map[string]grpcx.Method{
		"Snapshot": {Operation: core.OpSystem, Callers: []string{grpcx.AnyComponent}},
		"Read":     {Operation: core.OpSystem, Callers: []string{grpcx.AnyComponent}}})
	s.Register(&partnerv3.PartnerService_ServiceDesc, m, map[string]grpcx.Method{
		"CheckEligibility": {Operation: core.OpSystem, Callers: []string{grpcx.AnyComponent}},
		"Resolve":          {Operation: core.OpSystem, Callers: []string{grpcx.AnyComponent}}})
	return nil
}

// handler adapts a module's handler, which takes the shared base first, to what the SDK registers.
func (m *Module) handler(f func(*core.Base, *httpx.Context) error) httpx.Handler {
	return func(c *httpx.Context) error { return f(m.b, c) }
}
