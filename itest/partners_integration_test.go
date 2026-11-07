//go:build integration

package itest

import (
	"github.com/brickKit/be-sdk-go/v3/ids"
	assert "github.com/stretchr/testify/assert"
	require "github.com/stretchr/testify/require"
)

// partners: a partner once per idempotency key; the tax ID of another partner in the same country is pointed
// out, not refused (D22).
func (fx *fixture) partners() {
	t := fx.t
	// a partner, once per idempotency key; the tax ID of another partner in the same country is pointed out (D22)
	body := map[string]any{"kind": "organization", "name": "上海演示贸易有限公司", "country": "CN",
		"tax_id": "91310000MA1FL4XQ2N", "contacts": []map[string]any{{"name": "王小明", "role": "purchasing"}},
		"addresses": []map[string]any{{"kind": "billing", "lines": []string{"世纪大道 100 号"}, "city": "上海", "country": "CN"}}}
	key := "create-" + ids.New().String()
	code, a1 := fx.alice.do("POST", "/api/partner/partners", body, "Idempotency-Key", key)
	require.Equal(t, 200, code, a1)
	p1 := a1["partner"].(map[string]any)
	assert.Empty(t, a1["duplicate_candidates"])
	assert.NotEmpty(t, p1["contacts"].([]any)[0].(map[string]any)["id"], "a contact gets an ID")
	_, again := fx.alice.do("POST", "/api/partner/partners", body, "Idempotency-Key", key)
	assert.Equal(t, p1["id"], again["partner"].(map[string]any)["id"], "the same key: the same partner")
	code, _ = fx.alice.do("POST", "/api/partner/partners", body)
	assert.Equal(t, 400, code, "no idempotency key")
	code, a2 := fx.alice.do("POST", "/api/partner/partners", map[string]any{"kind": "organization", "name": "上海演示贸易（重复）",
		"country": "CN", "tax_id": "91310000 ma1fl4xq2n"}, "Idempotency-Key", "create-"+ids.New().String())
	require.Equal(t, 200, code, a2)
	assert.Equal(t, []any{p1["id"]}, a2["duplicate_candidates"], "the same tax ID in CN: pointed out, not refused")
	fx.pid = p1["id"].(string)
}

// partnerEdits: a stale version is refused; the keyword finds the pinyin initials; Bob sees nothing; merging is not
// in this version.
func (fx *fixture) partnerEdits() {
	t := fx.t
	// a stale version is refused; the keyword finds the pinyin initials; Bob sees nothing
	code, stale := fx.alice.do("PATCH", "/api/partner/partners/"+fx.pid, map[string]any{"version": 1, "name": "x"})
	assert.Equal(t, 409, code)
	assert.Equal(t, "VERSION_CONFLICT", stale["reason"])
	_, found := fx.alice.do("GET", "/api/partner/partners?q=shysmy", nil) // 上海演示贸易
	assert.Len(t, found["partners"], 2)
	code, _ = fx.bob.do("GET", "/api/partner/partners/"+fx.pid, nil)
	assert.Equal(t, 404, code)
	code, unsup := fx.alice.do("POST", "/api/partner/partners/"+fx.pid+"/merge", map[string]any{})
	assert.Equal(t, "CAPABILITY_UNSUPPORTED", unsup["reason"], code)
}
