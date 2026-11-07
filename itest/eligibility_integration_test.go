//go:build integration

package itest

import (
	assert "github.com/stretchr/testify/assert"
	require "github.com/stretchr/testify/require"
)

// eligibility: the boundary card's story: legal entity B has no customer role yet, so selling there is refused; a
// suspended role, a suspended partner (RF01); the supplier role is not in this version.
func (fx *fixture) eligibility() {
	t := fx.t
	// the boundary card's story: legal entity B has no customer role yet, so selling there is refused
	elig := func(le string) map[string]any {
		_, e := fx.alice.do("GET", "/api/partner/eligibility?purpose=sell&partner_id="+fx.pid+"&legal_entity_id="+le, nil)
		return e
	}
	assert.Equal(t, true, elig(fx.leA)["eligible"])
	assert.Equal(t, "role_not_opened", elig(fx.leB)["reason"])
	_, _ = fx.alice.do("POST", "/api/partner/partners/"+fx.pid+"/roles/customer/"+fx.leA+"/state", map[string]any{"state": "suspended"})
	assert.Equal(t, "role_suspended", elig(fx.leA)["reason"])
	_, _ = fx.alice.do("POST", "/api/partner/partners/"+fx.pid+"/roles/customer/"+fx.leA+"/state", map[string]any{"state": "active"})
	code, ps := fx.alice.do("POST", "/api/partner/partners/"+fx.pid+"/state", map[string]any{"state": "suspended"})
	require.Equal(t, 200, code, ps)
	assert.Equal(t, "partner_suspended", elig(fx.leA)["reason"], "a suspended partner takes no new references (RF01)")
	_, _ = fx.alice.do("POST", "/api/partner/partners/"+fx.pid+"/state", map[string]any{"state": "active"})
	code, _ = fx.alice.do("GET", "/api/partner/eligibility?purpose=purchase&partner_id="+fx.pid+"&legal_entity_id="+fx.leA, nil)
	assert.Equal(t, 501, code, "the supplier role is not in this version")
}
