package partner

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/launcher"
	"github.com/brickKit/be-sdk-go/v3/mergesafety"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTheManifestDeclaresWhatTheSDKReads: component.yaml's configSchema is exactly the keys the SDK reads.
func TestTheManifestDeclaresWhatTheSDKReads(t *testing.T) {
	raw, err := os.ReadFile("../component.yaml")
	require.NoError(t, err)
	if p := launcher.ManifestProblems(raw, Definition(), nil); len(p) > 0 {
		t.Errorf("component.yaml's configSchema differs from what the SDK reads:\n%v\nthe block:\n%s", p,
			launcher.ConfigSchemaYAML(Definition(), nil))
	}
}

// TestTheRoutesAreTheOpenAPIDocument: every route is in contract/openapi.yaml with its operation, and nothing else is.
func TestTheRoutesAreTheOpenAPIDocument(t *testing.T) {
	raw, err := os.ReadFile("../contract/openapi.yaml")
	require.NoError(t, err)
	var r httpx.Routes
	require.NoError(t, (&Module{}).Routes(&r))
	assert.Empty(t, httpx.OpenAPIProblems(raw, r.List()))
}

// TestThePayloadsFitTheirSchemas: what events and source reads carry is what contract/events promises.
func TestThePayloadsFitTheirSchemas(t *testing.T) {
	tax, ccy, terms := "91310000MA1FL4XQ2N", "CNY", "NET30"
	for typ, v := range map[string]any{
		PartnerChanged: Partner{ID: "01a15ab0-0000-7000-8000-0000000000c1", Kind: "organization", Name: "上海演示贸易有限公司",
			Names: map[string]string{"en": "Shanghai Demo Trading"}, Country: "CN", TaxID: &tax, State: "active",
			Contacts:  []Contact{{ID: "01a15ab0-0000-7000-8000-0000000000c2", Name: "王小明", Role: "purchasing", Email: "wang@example.com"}},
			Addresses: []Address{{ID: "01a15ab0-0000-7000-8000-0000000000c3", Kind: "billing", Lines: []string{"浦东新区世纪大道 100 号"}, City: "上海", Country: "CN"}},
			Version:   1, UpdatedAt: "2026-10-20T08:00:00.000000Z"},
		RoleChanged: Role{ID: "01a15ab0-0000-7000-8000-0000000000c4", PartnerID: "01a15ab0-0000-7000-8000-0000000000c1",
			Role: "customer", LegalEntityID: "01a15ab0-0000-7000-8000-0000000000a1", State: "active", DefaultCurrency: &ccy,
			PaymentTerms: &terms, Version: 2, UpdatedAt: "2026-10-20T08:00:00.000000Z"},
	} {
		c := jsonschema.NewCompiler()
		s, err := c.Compile("../contract/events/" + typ + ".schema.json")
		require.NoError(t, err, typ)
		b, err := json.Marshal(v)
		require.NoError(t, err)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		require.NoError(t, err)
		assert.NoError(t, s.Validate(doc), typ)
	}
}

// TestTheModuleIsMergeSafe: the module is compiled into the Go infra shell.
func TestTheModuleIsMergeSafe(t *testing.T) {
	findings, err := mergesafety.CheckDir(".")
	require.NoError(t, err)
	for _, f := range findings {
		t.Errorf("%v", f)
	}
}
