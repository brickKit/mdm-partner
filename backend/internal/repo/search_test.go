package repo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

// 关键字搜索 q 的规则（REST GET /customers?q=）：匹配 code 或 name，大小写
// 不敏感；code 或 name 以 q 开头的（前缀匹配）排在只是包含 q 的前面，同一档
// 内按建档时间倒序；空 q（含全空白）等于不过滤；仍然按游标翻页。
//
// 测试库会被别的测试与别的包并发写入，所以每条测试用一个本次运行独有的
// 关键字 token，只断言带 token 的那几行（空 q 那一条另用独占的时间窗口，见它的注释）。

func searchToken() string {
	return fmt.Sprintf("zq%x", time.Now().UnixNano())
}

// mk 建一个客户，幂等键带 token，保证每次运行都是新行。
func mk(t *testing.T, r *Repo, key, code, name string) *Customer {
	t.Helper()
	c, err := r.Create(context.Background(), CreateInput{IdempotencyKey: key, Code: code, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ids(cs []*Customer) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func TestList_q前缀匹配排在包含匹配之前(t *testing.T) {
	db := testDB(t)
	r := New(db, "mdm_customer_rw", "mdm_customer")
	tok := searchToken()

	contains := mk(t, r, tok+"-a", "", "甲"+tok+"公司")                    // 只是包含，最早建档
	codePrefix := mk(t, r, tok+"-b", strings.ToUpper(tok)+"-01", "乙公司") // code 前缀（大写）
	namePrefix := mk(t, r, tok+"-c", "", tok+"贸易")                      // name 前缀，最晚建档
	mk(t, r, tok+"-d", "", "毫不相干的客户")

	res, err := r.List(context.Background(), ListInput{Q: tok, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	got, want := ids(res.Customers), []string{namePrefix.ID, codePrefix.ID, contains.ID}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("期望前缀匹配（按建档时间倒序）在前、包含匹配在后：%v，实际 %v", want, got)
	}
}

func TestList_q大小写不敏感(t *testing.T) {
	db := testDB(t)
	r := New(db, "mdm_customer_rw", "mdm_customer")
	tok := searchToken()

	a := mk(t, r, tok+"-a", strings.ToUpper(tok), "大写编号客户")
	b := mk(t, r, tok+"-b", "", "Name "+strings.ToUpper(tok)+" Ltd")

	for _, q := range []string{tok, strings.ToUpper(tok)} {
		res, err := r.List(context.Background(), ListInput{Q: q, PageSize: 50})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := strings.Join(ids(res.Customers), ","), a.ID+","+b.ID; got != want {
			t.Fatalf("q=%q 期望 %s，实际 %s", q, want, got)
		}
	}
}

// backdate 把这几个客户的建档时间挪到同一个时刻 at，给只看 at 附近的窗口用。
func backdate(t *testing.T, db *sql.DB, at time.Time, ids ...string) {
	t.Helper()
	if err := besdk.WithTx(context.Background(), db, "mdm_customer_rw", "mdm_customer", func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.Exec(`UPDATE customers SET created_at = $1 WHERE id = $2`, at, id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// 空 q 与"不过滤"要比较整页结果，不能只看带 token 的行；而别的包同时在往测试库里写
// 客户，"最近一秒"的窗口里随时会多出别人的行，两次查询之间一插入就对不上。所以把本
// 测试的客户挪到十年前、精确到微秒的一个时刻，窗口只取这一刻前后 1 毫秒：窗口里只有
// 本测试的行。
func TestList_q为空等于不过滤(t *testing.T) {
	db := testDB(t)
	r := New(db, "mdm_customer_rw", "mdm_customer")
	tok := searchToken()
	unrelated := mk(t, r, tok+"-a", "", "不含关键字的客户")
	matching := mk(t, r, tok+"-b", "", tok+"公司")
	at := time.Now().AddDate(-10, 0, 0).Truncate(time.Microsecond)
	backdate(t, db, at, unrelated.ID, matching.ID)

	base := ListInput{PageSize: 200, CreatedAfter: at.Add(-time.Millisecond), CreatedBefore: at.Add(time.Millisecond)}
	plain, err := r.List(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Customers) != 2 {
		t.Fatalf("窗口里应只有本测试的 2 个客户，实际 %d 个：%v", len(plain.Customers), ids(plain.Customers))
	}
	for _, q := range []string{"", "   "} {
		in := base
		in.Q = q
		res, err := r.List(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := strings.Join(ids(res.Customers), ","), strings.Join(ids(plain.Customers), ","); got != want {
			t.Fatalf("q=%q 应该等于不过滤：期望 %s，实际 %s", q, want, got)
		}
		if !strings.Contains(","+strings.Join(ids(res.Customers), ",")+",", ","+unrelated.ID+",") {
			t.Fatalf("q=%q 时不含关键字的客户 %s 也应该在结果里", q, unrelated.ID)
		}
	}
}

func TestList_q里的通配符按字面匹配(t *testing.T) {
	db := testDB(t)
	r := New(db, "mdm_customer_rw", "mdm_customer")
	tok := searchToken()
	mk(t, r, tok+"-a", "", tok+"公司")

	for _, q := range []string{tok + "%", tok + "_", "%" + tok} {
		res, err := r.List(context.Background(), ListInput{Q: q, PageSize: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Customers) != 0 {
			t.Fatalf("q=%q 里的 %% / _ 应按字面匹配（没有客户名含它），实际命中 %d 条", q, len(res.Customers))
		}
	}
}

// 翻页跨过"前缀 → 包含"的分界：3 条前缀 + 2 条包含，每页 2 条，三页拼起来
// 必须恰好等于一次取全的结果，不重不漏、顺序一致。
func TestList_q与游标一起翻页不重不漏(t *testing.T) {
	db := testDB(t)
	r := New(db, "mdm_customer_rw", "mdm_customer")
	tok := searchToken()
	for i := 0; i < 2; i++ {
		mk(t, r, fmt.Sprintf("%s-c%d", tok, i), "", fmt.Sprintf("包含%s第%d家", tok, i))
	}
	for i := 0; i < 3; i++ {
		mk(t, r, fmt.Sprintf("%s-p%d", tok, i), "", fmt.Sprintf("%s前缀第%d家", tok, i))
	}

	all, err := r.List(context.Background(), ListInput{Q: tok, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Customers) != 5 {
		t.Fatalf("期望 5 条命中，实际 %d", len(all.Customers))
	}

	var paged []string
	cursor := ""
	for page := 0; page < 10; page++ {
		res, err := r.List(context.Background(), ListInput{Q: tok, PageSize: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		paged = append(paged, ids(res.Customers)...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if got, want := strings.Join(paged, ","), strings.Join(ids(all.Customers), ","); got != want {
		t.Fatalf("逐页拼起来应等于一次取全：期望 %s，实际 %s", want, got)
	}
}
