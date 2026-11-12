// Package partner is mdm/partner (boundary card mdm/partner): business partners (organizations and persons) with their
// formal contacts and addresses, and their roles opened by legal entity; the eligibility a new document needs and the
// batch resolution of protocol 07; the source methods of protocol 02 and full-state events. Step 6 of the
// representative deployment experiment: the customer role only; supplier, employee payee and affiliate roles,
// merging, bank accounts and erasure answer CAPABILITY_UNSUPPORTED (D75).
package partner

import (
	"embed"
	"io/fs"

	core "github.com/brickKit/mdm-partner/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/component"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/runtime"
)

// Identity of the component.
const (
	Version  = "3.0.41"
	GRPCPort = 9225
	// Org is the component the legal entities belong to: a required dependency.
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func migrations() fs.FS {
	sub, _ := fs.Sub(migrationFiles, "migrations")
	return sub
}

// Definition is the component as the launcher and the shells take it.
func Definition() component.Definition {
	return component.Definition{ID: core.ID, Version: Version, Migrations: migrations(), GRPCPort: GRPCPort,
		Streams: []events.Stream{{Name: core.Stream, Partitions: 4}}, Authz: core.Definitions(), Resend: core.OpResend, Recovery: core.OpRecover,
		New: func(rt *runtime.Runtime) (component.Module, error) { return New(rt) }}
}
