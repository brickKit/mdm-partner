//go:build integration

package itest

// The partner component against PostgreSQL and the bus over its real surfaces (L2). mdm/org is a stand-in that
// speaks its published contract (SourceService.Read) over mTLS, so the test needs no other component's code; the
// stand-in can also be stopped to check that an unreachable legal entity is not taken as a missing one. Needs
// BE_SDK_TEST_PG_ADMIN and BE_SDK_TEST_NATS_URL, as be-sdk-go's integration tests. The member listens on 28225 (user)
// and 9225 (system); the stand-in on 19093. Run: go test -tags integration ./itest/ (make test-integration); the index
// of the steps is itest/AGENTS.md.

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

	partner "github.com/brickKit/mdm-partner/v3/partner"

	core "github.com/brickKit/mdm-partner/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/config"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/launcher"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	orgv3 "github.com/brickKit/mdm-org/contract/v3/gen/go/be/mdm/org/v3"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	itPort   = "28225"
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

// fixture is the running member, the stand-in mdm/org and what one step leaves for the next.
type fixture struct {
	t             *testing.T
	ctx           context.Context
	admin         *pgx.Conn
	w             *sectest.World
	alice, bob    client
	leA, leB, leC string         // legal entities: A and B active, C suspended
	startOrg      func() func()  // starts the stand-in mdm/org, answers its stop
	stopOrg       func()         // stops the running one
	pid           string         // the partner (partners)
	role          map[string]any // its customer role in A (roles)
}

func TestPartnerEndToEnd(t *testing.T) {
	fx := start(t)
	fx.partners()
	fx.roles()
	fx.eligibility()
	fx.partnerEdits()
	fx.source()
	fx.outbox()
	fx.recoveryChecks()
}

// start makes the world and the stand-in mdm/org, migrates and runs the member.
func start(t *testing.T) *fixture {
	adminURL, natsURL := os.Getenv("BE_SDK_TEST_PG_ADMIN"), os.Getenv("BE_SDK_TEST_NATS_URL")
	if adminURL == "" || natsURL == "" {
		t.Fatal("BE_SDK_TEST_PG_ADMIN and BE_SDK_TEST_NATS_URL are needed")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	require.NoError(t, err)
	t.Cleanup(func() { admin.Close(ctx) })
	_, err = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+itSchema+" CASCADE; CREATE SCHEMA "+itSchema)
	require.NoError(t, err)
	u, err := url.Parse(adminURL)
	require.NoError(t, err)
	pw, _ := u.User.Password()
	w := sectest.New(t)
	w.GrantAll(sectest.Alice, core.Definitions())

	// the stand-in mdm/org: legal entity A active, B active, C suspended
	leA, leB, leC := ids.New().String(), ids.New().String(), ids.New().String()
	stand := &org{entities: map[string]string{leA: "active", leB: "active", leC: "suspended"}}
	startOrg := func() func() {
		ln, err := net.Listen("tcp", itOrg)
		require.NoError(t, err)
		srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(w.TLS(t, core.Org).ServerConfig())))
		orgv3.RegisterSourceServiceServer(srv, stand)
		go func() { _ = srv.Serve(ln) }()
		return srv.Stop
	}
	fx := &fixture{t: t, ctx: ctx, admin: admin, w: w, leA: leA, leB: leB, leC: leC, startOrg: startOrg}
	fx.stopOrg = startOrg()
	t.Cleanup(func() { fx.stopOrg() }) // the one running at the end (roles restarts it)

	portFree(t, itPort, itSystem)
	env := w.Env(t, core.ID)
	for k, v := range map[string]string{"COMPONENT_ID": core.ID, "COMPONENT_VERSION": partner.Version, "PORT": itPort,
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
	require.Equal(t, launcher.ExitOK, launcher.Run(ctx, partner.Definition(), migrate))
	sctx, stop := context.WithCancel(ctx)
	done := make(chan int, 1)
	go func() { done <- launcher.Run(sctx, partner.Definition(), opts) }()
	t.Cleanup(func() { stop(); <-done })
	require.Eventually(t, func() bool {
		r, err := http.Get(itBase + "/readyz")
		if err == nil {
			r.Body.Close()
		}
		return err == nil && r.StatusCode == 200
	}, 20*time.Second, 100*time.Millisecond)
	fx.alice, fx.bob = client{t, w.Bearer(sectest.Alice)}, client{t, w.Bearer(sectest.Bob)}
	return fx
}

// portFree fails at once when a port the member needs is taken: a running cell (host ports 18xxx) or another member
// would answer in its place, and the test would assert against another process.
func portFree(t *testing.T, addrs ...string) {
	for _, a := range addrs {
		if !strings.Contains(a, ":") {
			a = "localhost:" + a
		}
		ln, err := net.Listen("tcp", a)
		require.NoError(t, err, "port %s is taken: is a cell or another member running?", a)
		_ = ln.Close()
	}
}
