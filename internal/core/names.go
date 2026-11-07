package core

import (
	"time"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
)

// ID is the component's ID; Org is the dependency the roles check legal entities with.
const (
	ID  = "mdm/partner"
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
	// OpResend is the redelivery of this component's stream (be-protocol P6.38: every component with streams serves
	// it; any component of the deployment may call it).
	OpResend = "mdm.partner.system.resend"
	// OpRecover is a full recovery's checks (be-protocol chapter 14: the recovery tool only).
	OpRecover = "mdm.partner.system.recover"
	OpSystem  = "mdm.partner.system.read"
)

// Limits.
const (
	MaxBatch = 500 // IDs a Read or Resolve takes, records a Snapshot page gives (decision 0304)
	// W is how long an eligibility answer is evidence (protocol 07's starting value).
	W = 30 * time.Second
)

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
			{Key: OpResend, ResourceType: System, Purpose: "read", RiskClass: "low"},
			{Key: OpRecover, ResourceType: System, Purpose: "read", RiskClass: "low"},
		}}
}
