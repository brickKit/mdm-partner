//go:build integration

package itest

import (
	assert "github.com/stretchr/testify/assert"
	require "github.com/stretchr/testify/require"
)

// outbox: the customer role's full-state events: opened, suspended, resumed.
func (fx *fixture) outbox() {
	t, ctx := fx.t, fx.ctx
	var n int
	require.NoError(t, fx.admin.QueryRow(ctx, "SELECT count(*) FROM "+itSchema+".be_outbox WHERE record_id = $1", fx.role["id"]).Scan(&n))
	assert.Equal(t, 3, n, "opened, suspended, resumed")
}
