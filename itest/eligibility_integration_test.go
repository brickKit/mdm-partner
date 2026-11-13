//go:build integration

package itest

import (
	"time"

	assert "github.com/stretchr/testify/assert"
	require "github.com/stretchr/testify/require"
)

// eligibility: the boundary card's story: legal entity B has no customer role yet, so selling there is refused; a
// suspended role, a suspended partner (RF01); the supplier role (3.0.39) for purchases, apart from the customer role.
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

	// the supplier role (3.0.39): purchase needs it; it and the customer role are opened and suspended apart
	buy := func(le string) map[string]any {
		_, e := fx.alice.do("GET", "/api/partner/eligibility?purpose=purchase&partner_id="+fx.pid+"&legal_entity_id="+le, nil)
		return e
	}
	assert.Equal(t, "role_not_opened", buy(fx.leA)["reason"], "a customer is not a supplier")
	var sup map[string]any
	require.Eventually(t, func() bool { // the call breaker to mdm/org closes again after the roles step stopped it
		code, sup = fx.alice.do("PUT", "/api/partner/partners/"+fx.pid+"/roles/supplier/"+fx.leA,
			map[string]any{"default_currency": "EUR", "payment_terms": "NET60"})
		return code == 200
	}, 30*time.Second, 500*time.Millisecond, "%v", sup)
	assert.Equal(t, "supplier", sup["role"])
	assert.Equal(t, "NET60", sup["payment_terms"])
	assert.Equal(t, true, buy(fx.leA)["eligible"])
	assert.Equal(t, sup["id"], buy(fx.leA)["role_id"])
	code, again := fx.alice.do("PUT", "/api/partner/partners/"+fx.pid+"/roles/supplier/"+fx.leA,
		map[string]any{"default_currency": "EUR", "payment_terms": "NET60"})
	require.Equal(t, 200, code)
	assert.Equal(t, sup["version"], again["version"], "the same content again changes nothing")
	code, susp := fx.alice.do("PUT", "/api/partner/partners/"+fx.pid+"/roles/supplier/"+fx.leC, map[string]any{})
	assert.Equal(t, 409, code)
	assert.Equal(t, "LEGAL_ENTITY_NOT_USABLE", susp["reason"], "the same check of the legal entity")
	code, _ = fx.alice.do("POST", "/api/partner/partners/"+fx.pid+"/roles/supplier/"+fx.leA+"/state", map[string]any{"state": "suspended"})
	require.Equal(t, 200, code)
	assert.Equal(t, "role_suspended", buy(fx.leA)["reason"])
	assert.Equal(t, true, elig(fx.leA)["eligible"], "the customer role is not touched")
	_, _ = fx.alice.do("POST", "/api/partner/partners/"+fx.pid+"/roles/supplier/"+fx.leA+"/state", map[string]any{"state": "active"})
	_, list := fx.alice.do("GET", "/api/partner/partners/"+fx.pid+"/roles", nil)
	kinds := map[string]bool{}
	for _, r := range list["roles"].([]any) {
		kinds[r.(map[string]any)["role"].(string)] = true
	}
	assert.True(t, kinds["customer"] && kinds["supplier"], "both roles listed: %v", list)

	// the list by role (3.0.x, for the mobile aggregation's customers): partners with an active role of that kind,
	// in that legal entity when it is given
	listed := func(query string) bool {
		code, out := fx.alice.do("GET", "/api/partner/partners?limit=200&"+query, nil)
		require.Equal(t, 200, code, "%v", out)
		for _, p := range out["partners"].([]any) {
			if p.(map[string]any)["id"] == fx.pid {
				return true
			}
		}
		return false
	}
	assert.True(t, listed("role=customer&legal_entity_id="+fx.leA))
	assert.False(t, listed("role=customer&legal_entity_id="+fx.leB), "no customer role in B")
	assert.True(t, listed("role=supplier&legal_entity_id="+fx.leA))
	assert.True(t, listed("role=customer"), "in any legal entity")
	// the supplier role, so the customer role's events stay as the outbox step counts them
	_, _ = fx.alice.do("POST", "/api/partner/partners/"+fx.pid+"/roles/supplier/"+fx.leA+"/state", map[string]any{"state": "suspended"})
	assert.False(t, listed("role=supplier&legal_entity_id="+fx.leA), "a suspended role does not count")
	_, _ = fx.alice.do("POST", "/api/partner/partners/"+fx.pid+"/roles/supplier/"+fx.leA+"/state", map[string]any{"state": "active"})
	code, bad := fx.alice.do("GET", "/api/partner/partners?role=vendor", nil)
	assert.Equal(t, 400, code, "%v", bad)
	code, bad = fx.alice.do("GET", "/api/partner/partners?legal_entity_id="+fx.leA, nil)
	assert.Equal(t, 400, code, "a legal entity without a role: %v", bad)
}
