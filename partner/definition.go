// Package partner is mdm/partner (boundary card mdm/partner): business partners (organizations and persons) with their
// formal contacts and addresses, and their roles opened by legal entity; the eligibility a new document needs and the
// batch resolution of protocol 07; the source methods of protocol 02 and full-state events. Step 6 of the
// representative deployment experiment: the customer role only; supplier, employee payee and affiliate roles,
// merging, bank accounts and erasure answer CAPABILITY_UNSUPPORTED (D75).
package partner

import (
	"embed"
	"io/fs"
	"time"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/component"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/runtime"
)

// Identity of the component.
const (
	ID       = "mdm/partner"
	Version  = "3.0.6"
	GRPCPort = 9225
	// Org is the component the legal entities belong to: a required dependency.
	Org = "mdm/org"
)

// The stream and its event types (contract/events).
const (
	Stream         = "mdm.partner"
	PartnerChanged = "mdm.partner.changed.v1"
	RoleChanged    = "mdm.partner_role.changed.v1"
)

// Resources and operations (contract/openapi.yaml names each route's operation).
const (
	Partners   = "mdm.partner"
	OpRead     = "mdm.partner.read"
	OpCreate   = "mdm.partner.create"
	OpEdit     = "mdm.partner.edit"
	OpMerge    = "mdm.partner.merge"
	Roles      = "mdm.partner_role"
	OpRoleRead = "mdm.partner_role.read"
	OpRole     = "mdm.partner_role.manage"
	System     = "mdm.partner.system"
	OpSystem   = "mdm.partner.system.read"
)

// Limits.
const (
	MaxBatch = 500 // IDs a Read or Resolve takes, records a Snapshot page gives (decision 0304)
	// W is how long an eligibility answer is evidence (protocol 07's starting value).
	W = 30 * time.Second
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func migrations() fs.FS {
	sub, _ := fs.Sub(migrationFiles, "migrations")
	return sub
}

// Definitions are the component's resources and operations. No dimensions in step 6, declared as such.
func Definitions() *sdkauthz.Definitions {
	none := map[string]sdkauthz.Dimension{}
	fields := func(names ...string) map[string]sdkauthz.Field {
		out := map[string]sdkauthz.Field{}
		for _, n := range names {
			out[n] = sdkauthz.Field{Class: sdkauthz.Ordinary}
		}
		return out
	}
	return &sdkauthz.Definitions{Component: ID,
		Resources: []sdkauthz.Resource{
			{Type: Partners, ViewOperation: OpRead, Dimensions: none, Fields: fields("id", "kind", "name", "names", "country",
				"tax_id", "state", "contacts", "addresses", "version", "updated_at")},
			{Type: Roles, ViewOperation: OpRoleRead, Dimensions: none, Fields: fields("id", "partner_id", "role",
				"legal_entity_id", "state", "default_currency", "payment_terms", "version", "updated_at")},
			{Type: System, ViewOperation: OpSystem, Dimensions: none, Fields: map[string]sdkauthz.Field{}},
		},
		Operations: []sdkauthz.Operation{
			{Key: OpRead, ResourceType: Partners, Purpose: "read", RiskClass: "low"},
			{Key: OpCreate, ResourceType: Partners, Purpose: "action", RiskClass: "low", SideEffects: []string{"business_write"}, Audit: true},
			{Key: OpEdit, ResourceType: Partners, Purpose: "manage", RiskClass: "low", SideEffects: []string{"business_write"}, Audit: true},
			{Key: OpMerge, ResourceType: Partners, Purpose: "manage", RiskClass: "high", SideEffects: []string{"business_write"}, Audit: true},
			{Key: OpRoleRead, ResourceType: Roles, Purpose: "read", RiskClass: "low"},
			{Key: OpRole, ResourceType: Roles, Purpose: "manage", RiskClass: "low", SideEffects: []string{"business_write"}, Audit: true},
			{Key: OpSystem, ResourceType: System, Purpose: "read", RiskClass: "low"},
		}}
}

// Definition is the component as the launcher and the shells take it.
func Definition() component.Definition {
	return component.Definition{ID: ID, Version: Version, Migrations: migrations(), GRPCPort: GRPCPort,
		Streams: []events.Stream{{Name: Stream, Partitions: 4}}, Authz: Definitions(),
		New: func(rt *runtime.Runtime) (component.Module, error) { return New(rt) }}
}
