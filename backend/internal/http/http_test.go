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
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib"

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

	c, err := r.Create(ctx, repo.CreateInput{IdempotencyKey: "http-window-001", Code: "C-HTTP-WINDOW", Name: "老客户"})
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
