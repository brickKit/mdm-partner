package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/mdm-customer/v2/backend/internal/repo"
	_ "github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func testDB(t *testing.T) *sql.DB {
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
	return db
}

var keySeq int64

// runKey 返回本次运行独有的幂等键 / 客户编号。测试库跨运行保留数据：固定的
// 幂等键从第二次运行起命中回放，写路径根本不执行；固定的 code 会撞唯一索引。
func runKey(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), atomic.AddInt64(&keySeq, 1))
}

func newTestService(t *testing.T) (*Service, *repo.Repo, *sql.DB) {
	t.Helper()
	db := testDB(t)
	r := repo.New(db, "mdm_customer_rw", "mdm_customer")
	return New(r, slog.Default()), r, db
}

func TestUpdate_版本不一致时拒绝(t *testing.T) {
	svc, r, _ := newTestService(t)
	ctx := context.Background()

	c, err := r.Create(ctx, repo.CreateInput{
		IdempotencyKey: runKey("svc-update"), Code: runKey("C-SVC-UPDATE"), Name: "旧名字"})
	if err != nil {
		t.Fatal(err)
	}

	// 乐观锁：version 与库里不一致则拒绝，不许写穿
	_, err = svc.Update(ctx, UpdateInput{
		IdempotencyKey: runKey("svc-update"), ID: c.ID, Version: c.Version + 1, Name: "新名字"})
	if err == nil {
		t.Fatal("version 不一致时应该拒绝，实际没报错")
	}

	got, _, err := r.BatchGet(ctx, []string{c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "旧名字" {
		t.Fatalf("version 冲突时不该写穿，实际：%+v", got)
	}
}

// TestSetStatus_两个方向都允许流转：DISABLED 不是终态，客户可能重新建立业务
// 关系，允许流转回 ACTIVE；两个方向都只受乐观锁约束。没有终态，也就没有归档。
func TestSetStatus_两个方向都允许流转(t *testing.T) {
	svc, r, _ := newTestService(t)
	ctx := context.Background()

	c, err := r.Create(ctx, repo.CreateInput{
		IdempotencyKey: runKey("svc-status"), Code: runKey("C-SVC-STATUS"), Name: "状态流转测试"})
	if err != nil {
		t.Fatal(err)
	}

	disabled, err := svc.SetStatus(ctx, SetStatusInput{
		IdempotencyKey: runKey("svc-status"), ID: c.ID, Version: c.Version, Status: "DISABLED"})
	if err != nil {
		t.Fatalf("ACTIVE → DISABLED 应该允许：%v", err)
	}

	reactivated, err := svc.SetStatus(ctx, SetStatusInput{
		IdempotencyKey: runKey("svc-status"), ID: c.ID, Version: disabled.Version, Status: "ACTIVE"})
	if err != nil {
		t.Fatalf("DISABLED → ACTIVE 应该允许（不是终态），实际报错：%v", err)
	}
	if reactivated.Status != "ACTIVE" {
		t.Fatalf("期望状态回到 ACTIVE，实际 %q", reactivated.Status)
	}
}

func TestSetStatus_停用时发出disabled事件(t *testing.T) {
	svc, r, db := newTestService(t)
	ctx := context.Background()

	c, err := r.Create(ctx, repo.CreateInput{
		IdempotencyKey: runKey("svc-event"), Code: runKey("C-SVC-EVENT"), Name: "事件测试"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.SetStatus(ctx, SetStatusInput{
		IdempotencyKey: runKey("svc-event"), ID: c.ID, Version: c.Version, Status: "DISABLED"})
	if err != nil {
		t.Fatal(err)
	}

	var n int
	if err := besdk.WithTx(ctx, db, "mdm_customer_rw", "mdm_customer",
		func(tx *sql.Tx) error {
			return tx.QueryRow(
				`SELECT count(*) FROM event_outbox
					WHERE subject = 'mdm.customer.disabled.v1' AND aggregate_id = $1 AND version >= $2`,
				c.ID, updated.Version).Scan(&n)
		}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("期望落 1 条 mdm.customer.disabled.v1，实际 %d 条", n)
	}
}

// TestSetStatus_重新启用时发出updated事件而不是专门事件：事件契约
// （contracts/events/customer.events.json）只有 created / updated / disabled，
// 没有 "enabled"。重新启用复用 updated（payload 带 status）；新增 subject 要先
// 改契约，不能在实现里顺手发明。
func TestSetStatus_重新启用时发出updated事件而不是专门事件(t *testing.T) {
	svc, r, db := newTestService(t)
	ctx := context.Background()

	c, err := r.Create(ctx, repo.CreateInput{
		IdempotencyKey: runKey("svc-reenable"), Code: runKey("C-SVC-REENABLE"), Name: "重新启用测试"})
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := svc.SetStatus(ctx, SetStatusInput{
		IdempotencyKey: runKey("svc-reenable"), ID: c.ID, Version: c.Version, Status: "DISABLED"})
	if err != nil {
		t.Fatal(err)
	}
	reactivated, err := svc.SetStatus(ctx, SetStatusInput{
		IdempotencyKey: runKey("svc-reenable"), ID: c.ID, Version: disabled.Version, Status: "ACTIVE"})
	if err != nil {
		t.Fatal(err)
	}

	var n int
	if err := besdk.WithTx(ctx, db, "mdm_customer_rw", "mdm_customer",
		func(tx *sql.Tx) error {
			return tx.QueryRow(
				`SELECT count(*) FROM event_outbox
					WHERE subject = 'mdm.customer.updated.v1' AND aggregate_id = $1 AND version >= $2`,
				c.ID, reactivated.Version).Scan(&n)
		}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("重新启用应该发 mdm.customer.updated.v1（不是专门的 enabled 事件），实际匹配 %d 条", n)
	}
}

// name 是唯一没有兜底的必填字段（code 留空会自动编号，见 repo_test.go 的
// TestCreate_code留空时自动生成），校验必须发生在落库之前。
func TestCreate_name为空时拒绝且不落库(t *testing.T) {
	svc, _, db := newTestService(t)
	ctx := context.Background()

	code := runKey("C-L3-NAME-EMPTY")
	_, err := svc.Create(ctx, repo.CreateInput{
		IdempotencyKey: runKey("svc-l3-name-empty"), Code: code, Name: ""})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("name 为空应该拒绝并返回 ErrInvalidArgument，实际：%v", err)
	}

	var n int
	if err := besdk.WithTx(ctx, db, "mdm_customer_rw", "mdm_customer",
		func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT count(*) FROM customers WHERE code = $1`, code).Scan(&n)
		}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("校验应该发生在落库之前，实际库里已经有 %d 条", n)
	}
}

func TestCreate_creditLimit为负数时拒绝(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, repo.CreateInput{
		IdempotencyKey: runKey("svc-l3-credit-neg"), Code: runKey("C-L3-CREDIT-NEG"), Name: "负数额度测试", CreditLimit: "-1"})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("credit_limit 为负数应该拒绝并返回 ErrInvalidArgument，实际：%v", err)
	}
}

// TestCreate_creditLimit为0时允许 是上面那条的边界对照组：0 是合法值
// （新客户还没有信用额度是正常状态），不能因为校验"不能为负"顺手把 0
// 也拦下来。
func TestCreate_creditLimit为0时允许(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	c, err := svc.Create(ctx, repo.CreateInput{
		IdempotencyKey: runKey("svc-l3-credit-zero"), Code: runKey("C-L3-CREDIT-ZERO"), Name: "零额度测试", CreditLimit: "0"})
	if err != nil {
		t.Fatalf("credit_limit 为 0 应该允许，实际报错：%v", err)
	}
	if c.CreditLimit != "0.00" {
		t.Fatalf("期望落库后是 0.00，实际 %q", c.CreditLimit)
	}
}

// TestList_非法cursor是调用方的错返回InvalidArgument：游标是客户端原样
// 回传的字符串，被截断或篡改是调用方传错了参数，应该 400，不是 500。
func TestList_非法cursor是调用方的错返回InvalidArgument(t *testing.T) {
	svc, _, _ := newTestService(t)
	_, err := svc.List(context.Background(), repo.ListInput{Cursor: "不是base64"})
	if err == nil {
		t.Fatal("非法 cursor 应该报错")
	}
	if code := status.Code(ToStatus(err)); code != codes.InvalidArgument {
		t.Fatalf("非法 cursor 应映射成 InvalidArgument，实际 %v（%v）", code, err)
	}
}

// badCreditLimits 是 credit_limit 不该接受的写法。strconv.ParseFloat 放行其中的
// "NaN"、"Inf"、"1e3"、"0x1p4"：NaN 存进 NUMERIC(18,2) 后排在所有数之上，拿它当
// 额度上限的检查（erp/sales 的信用检查、erp/finance 的应收）对这个客户恒真；Inf
// 溢出成 500；"1e3" 是浮点写法；多于 2 位小数会被列类型悄悄舍入（"1.234" 存成
// 1.23）；17 位整数超出 NUMERIC(18,2)；负数没有意义。
var badCreditLimits = []string{
	"NaN", "nan", "Inf", "-Inf", "+Inf", "Infinity", "1e3", "0x1p4",
	" 1", "1 ", "+1", "1.", ".5", "1.234", "0.001", "-1", "-0.01", "abc",
	"12345678901234567",
}

func countByCode(t *testing.T, db *sql.DB, code string) int {
	t.Helper()
	var n int
	if err := besdk.WithTx(context.Background(), db, "mdm_customer_rw", "mdm_customer",
		func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT count(*) FROM customers WHERE code = $1`, code).Scan(&n)
		}); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestCreditLimit_非十进制写法一律是InvalidArgument且不落库：Create 与 Update 的
// credit_limit 只收严格的十进制字符串（非负、最多 16 位整数、2 位小数）。被拒绝的
// Create 一行不写；被拒绝的 Update 不改库里的额度与 version。
func TestCreditLimit_非十进制写法一律是InvalidArgument且不落库(t *testing.T) {
	svc, r, db := newTestService(t)
	ctx := context.Background()

	base, err := r.Create(ctx, repo.CreateInput{
		IdempotencyKey: runKey("svc-credit-base"), Code: runKey("C-CREDIT-BASE"), Name: "额度校验基线", CreditLimit: "100.00"})
	if err != nil {
		t.Fatal(err)
	}

	for _, v := range badCreditLimits {
		code := runKey("C-CREDIT-BAD")
		_, err := svc.Create(ctx, repo.CreateInput{
			IdempotencyKey: runKey("svc-credit-bad"), Code: code, Name: "额度校验", CreditLimit: v})
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("Create credit_limit=%q 应该是 ErrInvalidArgument，实际：%v", v, err)
		}
		if n := countByCode(t, db, code); n != 0 {
			t.Errorf("Create credit_limit=%q 被拒绝时不该落库，实际 %d 条", v, n)
		}
		_, err = svc.Update(ctx, UpdateInput{
			IdempotencyKey: runKey("svc-credit-bad-upd"), ID: base.ID, Version: base.Version,
			Name: base.Name, CreditLimit: v})
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("Update credit_limit=%q 应该是 ErrInvalidArgument，实际：%v", v, err)
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
		t.Fatalf("被拒绝的 Update 不该改库，期望 100.00 / version %d，实际 %s / %d",
			base.Version, got[0].CreditLimit, got[0].Version)
	}
}

// TestCreditLimit_合法十进制照常落库 是上面那条的对照组：整数、1 到 2 位小数、
// 16 位整数的上限、前导零都是合法写法，落库后按 NUMERIC(18,2) 规整成两位小数。
func TestCreditLimit_合法十进制照常落库(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	for in, want := range map[string]string{
		"0": "0.00", "12": "12.00", "1.5": "1.50", "0.01": "0.01", "007": "7.00",
		"9999999999999999.99": "9999999999999999.99",
	} {
		c, err := svc.Create(ctx, repo.CreateInput{
			IdempotencyKey: runKey("svc-credit-ok"), Code: runKey("C-CREDIT-OK"), Name: "合法额度", CreditLimit: in})
		if err != nil {
			t.Errorf("credit_limit=%q 应该允许，实际报错：%v", in, err)
			continue
		}
		if c.CreditLimit != want {
			t.Errorf("credit_limit=%q 落库后期望 %q，实际 %q", in, want, c.CreditLimit)
		}
	}
}
