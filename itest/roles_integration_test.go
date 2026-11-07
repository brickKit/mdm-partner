//go:build integration

package itest

import (
	"github.com/brickKit/be-sdk-go/v3/ids"
	assert "github.com/stretchr/testify/assert"
	require "github.com/stretchr/testify/require"
)

// roles: the customer role by legal entity; mdm/org says whether the legal entity is usable, and unreachable is not
// missing (protocol 07).
func (fx *fixture) roles() {
	t := fx.t
	// the customer role by legal entity; mdm/org says whether the legal entity is usable
	code, role := fx.alice.do("PUT", "/api/partner/partners/"+fx.pid+"/roles/customer/"+fx.leA,
		map[string]any{"default_currency": "CNY", "payment_terms": "NET30"})
	require.Equal(t, 200, code, role)
	code, susp := fx.alice.do("PUT", "/api/partner/partners/"+fx.pid+"/roles/customer/"+fx.leC, map[string]any{})
	assert.Equal(t, 409, code)
	assert.Equal(t, "LEGAL_ENTITY_NOT_USABLE", susp["reason"], "the legal entity is suspended")
	code, missing := fx.alice.do("PUT", "/api/partner/partners/"+fx.pid+"/roles/customer/"+ids.New().String(), map[string]any{})
	assert.Equal(t, 409, code)
	assert.Equal(t, "LEGAL_ENTITY_NOT_USABLE", missing["reason"])
	fx.stopOrg()
	code, down := fx.alice.do("PUT", "/api/partner/partners/"+fx.pid+"/roles/customer/"+fx.leB, map[string]any{})
	assert.NotEqual(t, 200, code, "mdm/org unreachable: the role is not opened")
	assert.NotEqual(t, "LEGAL_ENTITY_NOT_USABLE", down["reason"], "unreachable is not missing (protocol 07)")
	fx.stopOrg = fx.startOrg()
	fx.role = role
}
