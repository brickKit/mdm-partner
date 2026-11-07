//go:build integration

package itest

import (
	"github.com/brickKit/be-sdk-go/v3/ids"
	partnerv3 "github.com/brickKit/mdm-partner/contract/v3/gen/go/be/mdm/partner/v3"
	core "github.com/brickKit/mdm-partner/v3/internal/core"
	assert "github.com/stretchr/testify/assert"
	require "github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// source: the system surface over mTLS: eligibility, resolution, source reads.
func (fx *fixture) source() {
	t, ctx := fx.t, fx.ctx
	// the system surface over mTLS: eligibility, resolution, source reads
	conn, err := grpc.NewClient(itSystem, grpc.WithTransportCredentials(credentials.NewTLS(fx.w.TLS(t, "erp/sales").ClientConfig(core.ID))))
	require.NoError(t, err)
	defer conn.Close()
	ps2 := partnerv3.NewPartnerServiceClient(conn)
	e, err := ps2.CheckEligibility(ctx, &partnerv3.CheckEligibilityRequest{PartnerId: fx.pid, LegalEntityId: fx.leA, Purpose: partnerv3.Purpose_PURPOSE_SELL})
	require.NoError(t, err)
	assert.True(t, e.Eligible)
	assert.NotEmpty(t, e.RoleId)
	assert.NotEmpty(t, e.EvaluatedAt)
	res, err := ps2.Resolve(ctx, &partnerv3.ResolveRequest{Ids: []string{fx.pid, ids.New().String()}})
	require.NoError(t, err)
	assert.Equal(t, partnerv3.Resolution_RESOLUTION_CURRENT, res.Results[0].Resolution)
	assert.Equal(t, partnerv3.Resolution_RESOLUTION_NOT_FOUND, res.Results[1].Resolution)
	read, err := partnerv3.NewSourceServiceClient(conn).Read(ctx, &partnerv3.ReadRequest{
		Aggregate: partnerv3.Aggregate_AGGREGATE_PARTNER_ROLE, Ids: []string{fx.role["id"].(string)}})
	require.NoError(t, err)
	assert.Equal(t, partnerv3.ReadState_READ_STATE_EXISTS, read.Results[0].State)
	assert.Len(t, read.Stamp.Positions, 4)
}
