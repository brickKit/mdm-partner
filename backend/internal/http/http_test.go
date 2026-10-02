package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/otel/trace/noop"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/mdm-customer/v2/backend/internal/repo"
	"github.com/brickKit/mdm-customer/v2/backend/internal/service"
)

// 这里测的是 REST 层把查询参数接对了：handler 直接挂在测试自己的 engine 上，
// 不经过权限中间件（权限判定是 SDK 的事，由 SDK 自己的测试守）。

func testRepo(t *testing.T) (*repo.Repo, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("未设置 TEST_PG_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return repo.New(db, "mdm_customer_rw", "mdm_customer"), db
}

var keySeq int64

// runKey 返回本次运行独有的幂等键 / 客户编号。测试库跨运行保留数据：固定的
// 幂等键从第二次运行起命中回放，写路径根本不执行；固定的 code 会撞唯一索引。
func runKey(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), atomic.AddInt64(&keySeq, 1))
}

type listBody struct {
	Customers []customerDTO `json:"customers"`
	Next      string        `json:"next_cursor"`
}

func getList(t *testing.T, svc *service.Service, query url.Values) (int, listBody) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	eng := gin.New()
	eng.GET("/customers", listHandler(svc))
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/customers?"+query.Encode(), nil))
	var body listBody
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("响应不是合法 JSON：%v（%s）", err, w.Body.String())
		}
	}
	return w.Code, body
}

// TestList_REST按created_after放宽默认时间窗口：契约里 GET /customers 带
// created_after / created_before，不传时默认最近 90 天；调用方要看更早的
// 客户就显式放宽。一个 200 天前建档的客户，默认查不到，给了 created_after
// 就必须查得到。
func TestList_REST按created_after放宽默认时间窗口(t *testing.T) {
	r, db := testRepo(t)
	ctx := context.Background()
	svc := service.New(r, slog.Default())

	c, err := r.Create(ctx, repo.CreateInput{IdempotencyKey: runKey("http-window"), Code: runKey("C-HTTP-WINDOW"), Name: "老客户"})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-200 * 24 * time.Hour)
	if err := besdk.WithTx(ctx, db, "mdm_customer_rw", "mdm_customer", func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE customers SET created_at = $1 WHERE id = $2`, old, c.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	from := old.Add(-time.Hour).UTC().Format(time.RFC3339)
	to := old.Add(time.Hour).UTC().Format(time.RFC3339)
	code, body := getList(t, svc, url.Values{"created_after": {from}, "created_before": {to}, "page_size": {"200"}})
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", code)
	}
	found := false
	for _, got := range body.Customers {
		if got.ID == c.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("给了 created_after=%s created_before=%s，200 天前的客户 %s 应该在结果里（实际 %d 条）",
			from, to, c.ID, len(body.Customers))
	}
}

// TestList_REST的created_after格式不对返回400：时间参数写错是调用方的错，
// 不能悄悄忽略、按默认窗口返回一页看起来正常的结果。
func TestList_REST的created_after格式不对返回400(t *testing.T) {
	r, _ := testRepo(t)
	svc := service.New(r, slog.Default())
	code, _ := getList(t, svc, url.Values{"created_after": {"昨天"}})
	if code != http.StatusBadRequest {
		t.Fatalf("created_after 不是 RFC 3339 时间应返回 400，实际 %d", code)
	}
}

// TestList_REST把q传给列表：GET /customers?q= 只返回 code 或 name 含关键字
// 的客户（排序与翻页规则由 repo 的测试守）。
func TestList_REST把q传给列表(t *testing.T) {
	r, _ := testRepo(t)
	ctx := context.Background()
	svc := service.New(r, slog.Default())
	tok := fmt.Sprintf("hq%x", time.Now().UnixNano())

	hit, err := r.Create(ctx, repo.CreateInput{IdempotencyKey: tok + "-hit", Name: tok + "公司"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create(ctx, repo.CreateInput{IdempotencyKey: tok + "-miss", Name: "别的客户"}); err != nil {
		t.Fatal(err)
	}

	code, body := getList(t, svc, url.Values{"q": {tok}})
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", code)
	}
	if len(body.Customers) != 1 || body.Customers[0].ID != hit.ID {
		t.Fatalf("q=%s 期望只返回客户 %s，实际返回 %d 条", tok, hit.ID, len(body.Customers))
	}
}

// testRuntime 是 besdk.NewGinEngine 要的最小 Runtime：错误到状态码的映射、panic
// 恢复都在 SDK 的中间件里，REST 的状态码要经过它才是真实的。
func testRuntime() *besdk.Runtime {
	return &besdk.Runtime{
		Registry: besdk.NewRegistry(),
		Logger:   slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Tracer:   noop.NewTracerProvider().Tracer("test"),
	}
}

// engine 挂上与 RegisterRoutes 相同的路径与 handler，只是不带权限键：验签与权限
// 判定是 SDK 的事，由 SDK 自己的测试守；这里测参数校验与状态码。
func engine(svc *service.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	eng := besdk.NewGinEngine(testRuntime())
	g := eng.Group("/mdm/customer")
	g.GET("/customers/:id", getHandler(svc))
	g.POST("/customers", createHandler(svc))
	g.PATCH("/customers/:id", updateHandler(svc))
	g.POST("/customers/:id/status", setStatusHandler(svc))
	g.POST("/customers/:id/contacts", addContactHandler(svc))
	return eng
}

// do 发一次请求，返回状态码与响应体原文。
func do(t *testing.T, eng *gin.Engine, method, path string, body any) (int, string) {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req = httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// TestCreditLimit_REST的NaN_Inf_科学计数法等写法返回400：POST /customers 与
// PATCH /customers/{id} 的 credit_limit 不是严格的非负十进制（最多 16 位整数、2 位
// 小数）时答 400，不落库、不改库。
func TestCreditLimit_REST的NaN_Inf_科学计数法等写法返回400(t *testing.T) {
	r, _ := testRepo(t)
	svc := service.New(r, slog.Default())
	eng := engine(svc)
	ctx := context.Background()

	base, err := r.Create(ctx, repo.CreateInput{
		IdempotencyKey: runKey("http-credit-base"), Code: runKey("C-HTTP-CREDIT"), Name: "额度校验基线", CreditLimit: "100.00"})
	if err != nil {
		t.Fatal(err)
	}

	for _, v := range []string{"NaN", "Inf", "1e3", "-1", "1.234", "abc"} {
		code, body := do(t, eng, http.MethodPost, "/mdm/customer/customers", map[string]string{
			"idempotency_key": runKey("http-credit-bad"), "name": "额度校验", "credit_limit": v})
		if code != http.StatusBadRequest {
			t.Errorf("POST /customers credit_limit=%q 应返回 400，实际 %d：%s", v, code, body)
		}
		code, body = do(t, eng, http.MethodPatch, "/mdm/customer/customers/"+base.ID, map[string]any{
			"idempotency_key": runKey("http-credit-bad-upd"), "version": base.Version,
			"name": base.Name, "credit_limit": v})
		if code != http.StatusBadRequest {
			t.Errorf("PATCH /customers/{id} credit_limit=%q 应返回 400，实际 %d：%s", v, code, body)
		}
	}

	got, _, err := r.BatchGet(ctx, []string{base.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("客户 %s 应该还在，实际取回 %d 条", base.ID, len(got))
	}
	if got[0].CreditLimit != "100.00" || got[0].Version != base.Version {
		t.Fatalf("被拒绝的 PATCH 不该改库，期望 100.00 / version %d，实际 %s / %d",
			base.Version, got[0].CreditLimit, got[0].Version)
	}
}

// TestUpdate与SetStatus_REST版本过期答409：契约写明 Update 与 SetStatus 带的
// version 过期时失败（gRPC ABORTED），REST 上就是 409；库里不变。
func TestUpdate与SetStatus_REST版本过期答409(t *testing.T) {
	r, _ := testRepo(t)
	eng := engine(service.New(r, slog.Default()))
	ctx := context.Background()

	c, err := r.Create(ctx, repo.CreateInput{
		IdempotencyKey: runKey("http-stale"), Code: runKey("C-HTTP-STALE"), Name: "版本冲突测试"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/mdm/customer/customers/" + c.ID
	if code, body := do(t, eng, http.MethodPatch, path, map[string]any{
		"idempotency_key": runKey("http-stale-upd"), "version": c.Version + 1, "name": "新名字"}); code != http.StatusConflict {
		t.Errorf("PATCH 带过期 version 应返回 409，实际 %d：%s", code, body)
	}
	if code, body := do(t, eng, http.MethodPost, path+"/status", map[string]any{
		"idempotency_key": runKey("http-stale-status"), "version": c.Version + 1, "status": "DISABLED"}); code != http.StatusConflict {
		t.Errorf("POST /status 带过期 version 应返回 409，实际 %d：%s", code, body)
	}

	got, _, err := r.BatchGet(ctx, []string{c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "版本冲突测试" || got[0].Status != "ACTIVE" || got[0].Version != c.Version {
		t.Fatalf("版本冲突时不该改库，实际 %d 条", len(got))
	}
}

// TestCreate_REST重复code答409：显式传入的 code 已被别的客户占用，是调用方的错
// （导入时撞了编号），答 409，不是 500。
func TestCreate_REST重复code答409(t *testing.T) {
	r, _ := testRepo(t)
	eng := engine(service.New(r, slog.Default()))

	taken := runKey("C-HTTP-DUP")
	if _, err := r.Create(context.Background(), repo.CreateInput{
		IdempotencyKey: runKey("http-dup-first"), Code: taken, Name: "先来的客户"}); err != nil {
		t.Fatal(err)
	}
	if code, body := do(t, eng, http.MethodPost, "/mdm/customer/customers", map[string]string{
		"idempotency_key": runKey("http-dup-second"), "code": taken, "name": "后来的客户"}); code != http.StatusConflict {
		t.Fatalf("重复 code 应返回 409，实际 %d：%s", code, body)
	}
}

// TestCustomer_REST不存在或不是数字的id答404：按 id 找不到客户（含不是数字的
// id）时，读、改、改状态、加联系人都答 404——不是 409（版本冲突）也不是 500。
func TestCustomer_REST不存在或不是数字的id答404(t *testing.T) {
	r, _ := testRepo(t)
	eng := engine(service.New(r, slog.Default()))

	for _, id := range []string{"999999999", "abc"} {
		path := "/mdm/customer/customers/" + id
		if code, body := do(t, eng, http.MethodGet, path, nil); code != http.StatusNotFound {
			t.Errorf("GET id=%s 应返回 404，实际 %d：%s", id, code, body)
		}
		if code, body := do(t, eng, http.MethodPatch, path, map[string]any{
			"idempotency_key": runKey("http-404-upd"), "version": 1, "name": "x"}); code != http.StatusNotFound {
			t.Errorf("PATCH id=%s 应返回 404，实际 %d：%s", id, code, body)
		}
		if code, body := do(t, eng, http.MethodPost, path+"/status", map[string]any{
			"idempotency_key": runKey("http-404-status"), "version": 1, "status": "DISABLED"}); code != http.StatusNotFound {
			t.Errorf("POST /status id=%s 应返回 404，实际 %d：%s", id, code, body)
		}
		if code, body := do(t, eng, http.MethodPost, path+"/contacts", map[string]any{
			"idempotency_key": runKey("http-404-contact"), "contact": map[string]any{"name": "张三"}}); code != http.StatusNotFound {
			t.Errorf("POST /contacts id=%s 应返回 404，实际 %d：%s", id, code, body)
		}
	}
}
