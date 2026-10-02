package repo

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
	_ "github.com/jackc/pgx/v5/stdlib" // 与 SDK 同一个驱动（不用 lib/pq），驱动名注册为 "pgx"
)

// 这里没有 /healthz 的测试：它由 be-sdk-go 的 NewGinEngine 挂好（不查任何依赖），
// 由 SDK 自己的测试守；组件再注册一个同路径的 handler，Gin 会在启动时 panic。

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

// code 留空时自动生成 "C" + 6 位数字；显式传入时按传入的用（导入场景）。
func TestCreate_code留空时自动生成(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "mdm_customer_rw", "mdm_customer")

	c, err := r.Create(ctx, CreateInput{IdempotencyKey: "test-autocode-001", Name: "自动编号客户"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Code == "" {
		t.Fatal("code 留空时应该自动生成，实际还是空字符串")
	}
	if !strings.HasPrefix(c.Code, "C") || len(c.Code) != 7 {
		t.Fatalf(`期望形如 "C" + 6 位数字，实际得到 %q`, c.Code)
	}
}

func TestCreate_幂等(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "mdm_customer_rw", "mdm_customer")

	in := CreateInput{IdempotencyKey: "test-idem-001", Code: "C-001",
		Name: "Acme", CreditLimit: "100000.00"}
	a, err := r.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// 同一个 idempotency_key 再来一次：不许新建，必须返回同一条
	b, err := r.Create(ctx, in)
	if err != nil {
		t.Fatalf("幂等重试报错了：%v", err)
	}
	if a.ID != b.ID {
		t.Fatalf("幂等失效：第一次 %s，第二次 %s", a.ID, b.ID)
	}
}

// TestCreate_事件与业务数据同事务 验证 Outbox：业务写入与 PublishOutbox 在
// 同一事务里，任一方失败两边都不留痕迹。
//
// 故障注入用包内未导出的 testHookAfterOutbox（见 write.go），不在公开 API 上
// 挂只为测试存在的方法：生产二进制里它永远是 nil。
func TestCreate_事件与业务数据同事务(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "mdm_customer_rw", "mdm_customer")

	testHookAfterOutbox = func() error { return errors.New("注入的失败，验证回滚") }
	t.Cleanup(func() { testHookAfterOutbox = nil })

	_, err := r.Create(ctx, CreateInput{
		IdempotencyKey: "test-rollback-001", Code: "C-ROLLBACK", Name: "Rollback"})
	if err == nil {
		t.Fatal("期望注入的失败被返回")
	}

	var n int
	if err := besdk.WithTx(ctx, db, "mdm_customer_rw", "mdm_customer",
		func(tx *sql.Tx) error {
			return tx.QueryRow(
				`SELECT count(*) FROM event_outbox WHERE aggregate_id IN
					(SELECT id::text FROM customers WHERE code = $1)`,
				"C-ROLLBACK").Scan(&n)
		}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("业务回滚了但 outbox 留了 %d 条——说明事件不在同一事务里", n)
	}

	var m int
	if err := besdk.WithTx(ctx, db, "mdm_customer_rw", "mdm_customer",
		func(tx *sql.Tx) error {
			return tx.QueryRow(
				`SELECT count(*) FROM customers WHERE code = $1`, "C-ROLLBACK").Scan(&m)
		}); err != nil {
		t.Fatal(err)
	}
	if m != 0 {
		t.Fatalf("期望业务行也回滚，实际还留着 %d 条", m)
	}
}

// AddContact 的 customer_id 是从 Customer.ID（string）传下来的，插的是
// contacts.customer_id（bigint）列：用真库确认 pgx 按上下文推断参数类型，
// string 绑定 bigint 列不报错，并顺带验证幂等。
func TestAddContact_customer_id按字符串传也能正常插入(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "mdm_customer_rw", "mdm_customer")

	c, err := r.Create(ctx, CreateInput{IdempotencyKey: "test-contact-cust-001", Name: "联系人测试客户"})
	if err != nil {
		t.Fatal(err)
	}

	contact, err := r.AddContact(ctx, AddContactInput{
		IdempotencyKey: "test-contact-001", CustomerID: c.ID,
		Name: "张三", Phone: "13800000000", Primary: true,
	})
	if err != nil {
		t.Fatalf("AddContact 不该报错：%v", err)
	}
	if contact.ID == "" || contact.Name != "张三" {
		t.Fatalf("期望拿到新联系人，实际：%+v", contact)
	}

	// 幂等：同一个 idempotency_key 再来一次，不许新建
	again, err := r.AddContact(ctx, AddContactInput{
		IdempotencyKey: "test-contact-001", CustomerID: c.ID,
		Name: "张三", Phone: "13800000000", Primary: true,
	})
	if err != nil {
		t.Fatalf("幂等重试报错了：%v", err)
	}
	if again.ID != contact.ID {
		t.Fatalf("幂等失效：第一次 %s，第二次 %s", contact.ID, again.ID)
	}
}

// TestBatchGet_缺失的id不报错 测的不是 besdk.BatchGetRouted 本身（SDK 自己的
// 测试守），而是本组件把它接对了：真的 customers 表、真的扫描函数，缺失的 id
// 不报错、被分类进 missing。
func TestBatchGet_缺失的id不报错(t *testing.T) {
	db := testDB(t)
	r := New(db, "mdm_customer_rw", "mdm_customer")
	got, missing, err := r.BatchGet(context.Background(), []string{"999999999"})
	if err != nil {
		t.Fatalf("BatchGet 对缺失 id 不该报错：%v", err)
	}
	if len(got) != 0 || len(missing) != 1 {
		t.Fatalf("期望 0 命中 1 缺失，得到 %d/%d", len(got), len(missing))
	}
}

// TestList_未传时间范围时自动注入90天窗口 测的不是 besdk.ListWindow 本身（SDK
// 自己的测试守），而是本组件的 List 真的经过它，没有自己另拼一套逻辑。不连库。
func TestList_未传时间范围时自动注入90天窗口(t *testing.T) {
	q := buildListQuery(ListInput{PageSize: 20}) // 不传 CreatedAfter/CreatedBefore
	if q.From.IsZero() || q.To.IsZero() {
		t.Fatal("必须自动注入默认时间窗口，否则无条件的列表查询会扫全表")
	}
	days := q.To.Sub(q.From).Hours() / 24
	if days < 89 || days > 91 {
		t.Fatalf("默认窗口应约为 90 天，实际 %.1f 天", days)
	}
}

// TestList_最后一页返回空nextCursor而不是报错：List 多取一条判断有没有下一页，
// "没有多出来那一条"的分支只有真的翻到最后一页才走得到，要真库才测得出来。
func TestList_最后一页返回空nextCursor而不是报错(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "mdm_customer_rw", "mdm_customer")

	c, err := r.Create(ctx, CreateInput{IdempotencyKey: "test-lastpage-001", Name: "分页边界测试"})
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.List(ctx, ListInput{
		PageSize:      500,
		CreatedAfter:  c.CreatedAt.Add(-time.Second),
		CreatedBefore: c.CreatedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("已经是最后一页不该报错：%v", err)
	}
	if res.NextCursor != "" {
		t.Fatalf("已经是最后一页，期望 next_cursor 为空，实际 %q", res.NextCursor)
	}
}

// TestCreate_creditLimit超出精度时报错且不落库 是 L3 边界值测试：
// NUMERIC(18,2) 最多 16 位整数 + 2 位小数，17 位整数部分必然溢出。
// 数据库报错时整个事务回滚，不留下部分写入（幂等键、Outbox 都不留）。
func TestCreate_creditLimit超出精度时报错且不落库(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "mdm_customer_rw", "mdm_customer")

	_, err := r.Create(ctx, CreateInput{
		IdempotencyKey: "test-precision-001", Code: "C-PRECISION",
		Name: "精度溢出测试", CreditLimit: "999999999999999999.99", // 17 位整数，超出 NUMERIC(18,2)
	})
	if err == nil {
		t.Fatal("超出 NUMERIC(18,2) 精度应该报错，实际没报错")
	}

	var n int
	if err := besdk.WithTx(ctx, db, "mdm_customer_rw", "mdm_customer",
		func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT count(*) FROM customers WHERE code = $1`, "C-PRECISION").Scan(&n)
		}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("精度溢出应该让整个事务回滚，不留痕迹，实际库里有 %d 条", n)
	}
}

// TestCreate_并发相同idempotencyKey只落一条 是并发测试（-race 下跑）。
// Code 故意留空（走自动编号）：同一个显式 code 会先在 customers_code_uniq 上
// 撞车，测的就成了"code 唯一"而不是"幂等键去重"。
// command_idempotency.idempotency_key 是主键，并发下后提交的那个撞主键、整个
// 事务回滚（连同它插入的 customers 行），所以最终只落 1 条。断言不要求每个并发
// 调用都成功返回，只要求最终状态只有 1 条——这就是"幂等"承诺的内容。
func TestCreate_并发相同idempotencyKey只落一条(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "mdm_customer_rw", "mdm_customer")

	const key = "test-concurrent-idem-001"
	const n = 8
	var wg sync.WaitGroup
	oks := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := r.Create(ctx, CreateInput{IdempotencyKey: key, Name: "并发测试"})
			oks[i] = err == nil
		}(i)
	}
	wg.Wait()

	okCount := 0
	for _, ok := range oks {
		if ok {
			okCount++
		}
	}
	if okCount == 0 {
		t.Fatal("并发请求全部失败，至少应该有一个赢家")
	}

	var count int
	if err := besdk.WithTx(ctx, db, "mdm_customer_rw", "mdm_customer",
		func(tx *sql.Tx) error {
			return tx.QueryRow(`
				SELECT count(*) FROM customers c
				JOIN command_idempotency ci ON ci.result_id = c.id::text
				WHERE ci.idempotency_key = $1`, key).Scan(&count)
		}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("并发 %d 次相同 idempotency_key，应该只落 1 条 customers 记录，实际 %d 条", n, count)
	}
}
