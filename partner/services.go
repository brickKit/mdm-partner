package partner

import (
	"context"

	partnerv3 "github.com/brickKit/mdm-partner/contract/v3/gen/go/be/mdm/partner/v3"
	source "github.com/brickKit/mdm-partner/v3/internal/source"
)

// The gRPC methods of the system surface answer through internal/source.

// Snapshot is SourceService.Snapshot.
func (m *Module) Snapshot(ctx context.Context, in *partnerv3.SnapshotRequest) (*partnerv3.SnapshotResponse, error) {
	return source.Snapshot(m.b, ctx, in)
}

// Read is SourceService.Read.
func (m *Module) Read(ctx context.Context, in *partnerv3.ReadRequest) (*partnerv3.ReadResponse, error) {
	return source.Read(m.b, ctx, in)
}

// CheckEligibility is PartnerService.CheckEligibility.
func (m *Module) CheckEligibility(ctx context.Context, in *partnerv3.CheckEligibilityRequest) (*partnerv3.CheckEligibilityResponse, error) {
	return source.CheckEligibility(m.b, ctx, in)
}

// Resolve is PartnerService.Resolve.
func (m *Module) Resolve(ctx context.Context, in *partnerv3.ResolveRequest) (*partnerv3.ResolveResponse, error) {
	return source.Resolve(m.b, ctx, in)
}
