//go:build integration

package partner

// The partner component against PostgreSQL and the bus over its real surfaces (L2). mdm/org is a stand-in that
// speaks its published contract (SourceService.Read) over mTLS, so the test needs no other component's code; the
// stand-in can also be stopped to check that an unreachable legal entity is not taken as a missing one. Needs
// BE_SDK_TEST_PG_ADMIN and BE_SDK_TEST_NATS_URL, as be-sdk-go's integration tests. The member listens on 18225 (user)
// and 9225 (system); the stand-in on 19093. Run: go test -tags integration ./partner/

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brickKit/be-sdk-go/v3/config"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/launcher"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	orgv3 "github.com/brickKit/mdm-org/contract/v3/gen/go/be/mdm/org/v3"
	partnerv3 "github.com/brickKit/mdm-partner/contract/v3/gen/go/be/mdm/partner/v3"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	itPort   = "18225"
	itBase   = "http://localhost:" + itPort
	itSystem = "localhost:9225"
	itOrg    = "localhost:19093"
	itSchema = "mdm_partner_it"
)

// org is the stand-in mdm/org: legal entities by ID with their state.
type org struct {
	orgv3.UnimplementedSourceServiceServer
	mu       sync.Mutex
	entities map[string]string // id -> state
}

func (o *org) Read(_ context.Context, in *orgv3.ReadRequest) (*orgv3.ReadResponse, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := &orgv3.ReadResponse{Stamp: &orgv3.Stamp{}}
	for _, id := range in.GetIds() {
		state, ok := o.entities[id]
		if !ok {
			out.Results = append(out.Results, &orgv3.ReadResult{Id: id, State: orgv3.ReadState_READ_STATE_PURGED})
			continue
		}
		data, _ := json.Marshal(map[string]any{"id": id, "state": state})
		out.Results = append(out.Results, &orgv3.ReadResult{Id: id, State: orgv3.ReadState_READ_STATE_EXISTS, Version: 1, Data: data})
	}
	return out, nil
}

type client struct {
	t     *testing.T
	token string
}

func (c client) do(method, path string, body any, header ...string) (int, map[string]any) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, itBase+path, r)
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(c.t, err)
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func TestPartnerEndToEnd(t *testing.T) {
	adminURL, natsURL := os.Getenv("BE_SDK_TEST_PG_ADMIN"), os.Getenv("BE_SDK_TEST_NATS_URL")
	if adminURL == "" || natsURL == "" {
		t.Fatal("BE_SDK_TEST_PG_ADMIN and BE_SDK_TEST_NATS_URL are needed")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	require.NoError(t, err)
	defer admin.Close(ctx)
	_, err = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+itSchema+" CASCADE; CREATE SCHEMA "+itSchema)
	require.NoError(t, err)
	u, err := url.Parse(adminURL)
	require.NoError(t, err)
	pw, _ := u.User.Password()
	w := sectest.New(t)
	w.GrantAll(sectest.Alice, Definitions())

	// the stand-in mdm/org: legal entity A active, B active, C suspended
	leA, leB, leC := ids.New().String(), ids.New().String(), ids.New().String()
	stand := &org{entities: map[string]string{leA: "active", leB: "active", leC: "suspended"}}
	startOrg := func() func() {
		ln, err := net.Listen("tcp", itOrg)
		require.NoError(t, err)
		srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(w.TLS(t, Org).ServerConfig())))
		orgv3.RegisterSourceServiceServer(srv, stand)
		go func() { _ = srv.Serve(ln) }()
		return srv.Stop
	}
	stopOrg := startOrg()

	env := w.Env(t, ID)
	for k, v := range map[string]string{"COMPONENT_ID": ID, "COMPONENT_VERSION": Version, "PORT": itPort,
		"STOP_DRAIN_DELAY": "50ms", "PG_HOST": u.Hostname(), "PG_PORT": u.Port(), "PG_DATABASE": strings.TrimPrefix(u.Path, "/"),
		"PG_USER": u.User.Username(), "PG_PASSWORD": pw, "PG_SCHEMA": itSchema, "PG_OWNER_USER": u.User.Username(),
		"PG_OWNER_PASSWORD": pw, "BUS_URL": natsURL, "IMPLEMENTATION_INSTANCE": ids.New().String(),
		"MDM_ORG_GRPC_ENDPOINT": "http://" + itOrg} {
		env[k] = v
	}
	var logs bytes.Buffer
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	opts := launcher.Options{Stdout: &logs, Stderr: &logs,
		Env: config.Env{LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }, Read: os.ReadFile}}
	migrate := opts
	migrate.Args = []string{"migrate"}
	require.Equal(t, launcher.ExitOK, launcher.Run(ctx, Definition(), migrate))
	sctx, stop := context.WithCancel(ctx)
	done := make(chan int, 1)
	go func() { done <- launcher.Run(sctx, Definition(), opts) }()
	defer func() { stop(); <-done }()
	require.Eventually(t, func() bool {
		r, err := http.Get(itBase + "/readyz")
		if err == nil {
			r.Body.Close()
		}
		return err == nil && r.StatusCode == 200
	}, 20*time.Second, 100*time.Millisecond)
	alice, bob := client{t, w.Bearer(sectest.Alice)}, client{t, w.Bearer(sectest.Bob)}

	// a partner, once per idempotency key; the tax ID of another partner in the same country is pointed out (D22)
	body := map[string]any{"kind": "organization", "name": "上海演示贸易有限公司", "country": "CN",
		"tax_id": "91310000MA1FL4XQ2N", "contacts": []map[string]any{{"name": "王小明", "role": "purchasing"}},
		"addresses": []map[string]any{{"kind": "billing", "lines": []string{"世纪大道 100 号"}, "city": "上海", "country": "CN"}}}
	key := "create-" + ids.New().String()
	code, a1 := alice.do("POST", "/api/partner/partners", body, "Idempotency-Key", key)
	require.Equal(t, 200, code, a1)
	p1 := a1["partner"].(map[string]any)
	assert.Empty(t, a1["duplicate_candidates"])
	assert.NotEmpty(t, p1["contacts"].([]any)[0].(map[string]any)["id"], "a contact gets an ID")
	_, again := alice.do("POST", "/api/partner/partners", body, "Idempotency-Key", key)
	assert.Equal(t, p1["id"], again["partner"].(map[string]any)["id"], "the same key: the same partner")
	code, _ = alice.do("POST", "/api/partner/partners", body)
	assert.Equal(t, 400, code, "no idempotency key")
	code, a2 := alice.do("POST", "/api/partner/partners", map[string]any{"kind": "organization", "name": "上海演示贸易（重复）",
		"country": "CN", "tax_id": "91310000 ma1fl4xq2n"}, "Idempotency-Key", "create-"+ids.New().String())
	require.Equal(t, 200, code, a2)
	assert.Equal(t, []any{p1["id"]}, a2["duplicate_candidates"], "the same tax ID in CN: pointed out, not refused")
	pid := p1["id"].(string)

	// the customer role by legal entity; mdm/org says whether the legal entity is usable
	code, role := alice.do("PUT", "/api/partner/partners/"+pid+"/roles/customer/"+leA,
		map[string]any{"default_currency": "CNY", "payment_terms": "NET30"})
	require.Equal(t, 200, code, role)
	code, susp := alice.do("PUT", "/api/partner/partners/"+pid+"/roles/customer/"+leC, map[string]any{})
	assert.Equal(t, 409, code)
	assert.Equal(t, "LEGAL_ENTITY_NOT_USABLE", susp["reason"], "the legal entity is suspended")
	code, missing := alice.do("PUT", "/api/partner/partners/"+pid+"/roles/customer/"+ids.New().String(), map[string]any{})
	assert.Equal(t, 409, code)
	assert.Equal(t, "LEGAL_ENTITY_NOT_USABLE", missing["reason"])
	stopOrg()
	code, down := alice.do("PUT", "/api/partner/partners/"+pid+"/roles/customer/"+leB, map[string]any{})
	assert.NotEqual(t, 200, code, "mdm/org unreachable: the role is not opened")
	assert.NotEqual(t, "LEGAL_ENTITY_NOT_USABLE", down["reason"], "unreachable is not missing (protocol 07)")
	stopOrg = startOrg()
	defer stopOrg()

	// the boundary card's story: legal entity B has no customer role yet, so selling there is refused
	elig := func(le string) map[string]any {
		_, e := alice.do("GET", "/api/partner/eligibility?purpose=sell&partner_id="+pid+"&legal_entity_id="+le, nil)
		return e
	}
	assert.Equal(t, true, elig(leA)["eligible"])
	assert.Equal(t, "role_not_opened", elig(leB)["reason"])
	_, _ = alice.do("POST", "/api/partner/partners/"+pid+"/roles/customer/"+leA+"/state", map[string]any{"state": "suspended"})
	assert.Equal(t, "role_suspended", elig(leA)["reason"])
	_, _ = alice.do("POST", "/api/partner/partners/"+pid+"/roles/customer/"+leA+"/state", map[string]any{"state": "active"})
	code, ps := alice.do("POST", "/api/partner/partners/"+pid+"/state", map[string]any{"state": "suspended"})
	require.Equal(t, 200, code, ps)
	assert.Equal(t, "partner_suspended", elig(leA)["reason"], "a suspended partner takes no new references (RF01)")
	_, _ = alice.do("POST", "/api/partner/partners/"+pid+"/state", map[string]any{"state": "active"})
	code, _ = alice.do("GET", "/api/partner/eligibility?purpose=purchase&partner_id="+pid+"&legal_entity_id="+leA, nil)
	assert.Equal(t, 501, code, "the supplier role is not in this version")

	// a stale version is refused; the keyword finds the pinyin initials; Bob sees nothing
	code, stale := alice.do("PATCH", "/api/partner/partners/"+pid, map[string]any{"version": 1, "name": "x"})
	assert.Equal(t, 409, code)
	assert.Equal(t, "VERSION_CONFLICT", stale["reason"])
	_, found := alice.do("GET", "/api/partner/partners?q=shysmy", nil) // 上海演示贸易
	assert.Len(t, found["partners"], 2)
	code, _ = bob.do("GET", "/api/partner/partners/"+pid, nil)
	assert.Equal(t, 404, code)
	code, unsup := alice.do("POST", "/api/partner/partners/"+pid+"/merge", map[string]any{})
	assert.Equal(t, "CAPABILITY_UNSUPPORTED", unsup["reason"], code)

	// the system surface over mTLS: eligibility, resolution, source reads
	conn, err := grpc.NewClient(itSystem, grpc.WithTransportCredentials(credentials.NewTLS(w.TLS(t, "erp/sales").ClientConfig(ID))))
	require.NoError(t, err)
	defer conn.Close()
	ps2 := partnerv3.NewPartnerServiceClient(conn)
	e, err := ps2.CheckEligibility(ctx, &partnerv3.CheckEligibilityRequest{PartnerId: pid, LegalEntityId: leA, Purpose: partnerv3.Purpose_PURPOSE_SELL})
	require.NoError(t, err)
	assert.True(t, e.Eligible)
	assert.NotEmpty(t, e.RoleId)
	assert.NotEmpty(t, e.EvaluatedAt)
	res, err := ps2.Resolve(ctx, &partnerv3.ResolveRequest{Ids: []string{pid, ids.New().String()}})
	require.NoError(t, err)
	assert.Equal(t, partnerv3.Resolution_RESOLUTION_CURRENT, res.Results[0].Resolution)
	assert.Equal(t, partnerv3.Resolution_RESOLUTION_NOT_FOUND, res.Results[1].Resolution)
	read, err := partnerv3.NewSourceServiceClient(conn).Read(ctx, &partnerv3.ReadRequest{
		Aggregate: partnerv3.Aggregate_AGGREGATE_PARTNER_ROLE, Ids: []string{role["id"].(string)}})
	require.NoError(t, err)
	assert.Equal(t, partnerv3.ReadState_READ_STATE_EXISTS, read.Results[0].State)
	assert.Len(t, read.Stamp.Positions, 4)

	var n int
	require.NoError(t, admin.QueryRow(ctx, "SELECT count(*) FROM "+itSchema+".be_outbox WHERE record_id = $1", role["id"]).Scan(&n))
	assert.Equal(t, 3, n, "opened, suspended, resumed")
}
