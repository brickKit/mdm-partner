package service

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/mdm-customer/v2/backend/internal/repo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 自动编号（"C" + 6 位序列值）与显式 code 共用同一个编号空间：显式 code 可以先占掉
// 序列将来才会发出的值（从旧系统导入、重新导入本系统导出的编号）。不带 code 的 Create
// 撞上这种编号，是服务端的编号撞车，不是调用方的错：顺延到下一个序列值，不能答 409
// "code 已被占用"——调用方根本没传 code。

// seqValue 读 customers_id_seq 当前的 last_value。
func seqValue(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var v int64
	if err := besdk.WithTx(context.Background(), db, "mdm_customer_rw", "mdm_customer",
		func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT last_value FROM customers_id_seq`).Scan(&v)
		}); err != nil {
		t.Fatal(err)
	}
	return v
}

// setSeqAtLeast 把序列推到不小于 v（只往前，不往回拨：别的测试已经发出去的值不会重发）。
func setSeqAtLeast(t *testing.T, db *sql.DB, v int64) {
	t.Helper()
	if err := besdk.WithTx(context.Background(), db, "mdm_customer_rw", "mdm_customer",
		func(tx *sql.Tx) error {
			_, err := tx.Exec(`SELECT setval('customers_id_seq',
				GREATEST((SELECT last_value FROM customers_id_seq), $1))`, v)
			return err
		}); err != nil {
		t.Fatal(err)
	}
}

// occupyNextAutoCodes 让序列接下来要发的 n 个值对应的 "C%06d" 全部已被显式 code 占用，
// 返回跳板值 base：序列下一次发 base+1，base+1..base+n 都已被占。
//
// 显式 Create 自己也消耗序列（id 是 BIGSERIAL），所以先显式建、再把序列拨到 base；
// base 比当前值多留 100，给并行跑的别的测试包消耗。结束时把序列推过整个占用区，免得
// 剩下的占用编号拖累之后的自动编号。
func occupyNextAutoCodes(t *testing.T, r *repo.Repo, db *sql.DB, n int) int64 {
	t.Helper()
	base := seqValue(t, db) + 100
	for i := 1; i <= n; i++ {
		code := fmt.Sprintf("C%06d", base+int64(i))
		if _, err := r.Create(context.Background(), repo.CreateInput{
			IdempotencyKey: runKey("autocode-occupy"), Code: code, Name: "显式占用 " + code}); err != nil {
			t.Fatalf("显式占用 %s：%v", code, err)
		}
	}
	if now := seqValue(t, db); now >= base {
		t.Fatalf("序列在准备期间被别的测试推到 %d，越过了跳板值 %d", now, base)
	}
	setSeqAtLeast(t, db, base)
	t.Cleanup(func() { setSeqAtLeast(t, db, base+int64(n)) })
	return base
}

// TestCreate_自动编号撞上显式占用的code时顺延(I1)：序列接下来的两个值都已被显式 code
// 占用，不带 code 的 Create 照样成功，编号落在两者之后。
func TestCreate_自动编号撞上显式占用的code时顺延(t *testing.T) {
	db := testDB(t)
	var logs bytes.Buffer
	r := repo.New(db, "mdm_customer_rw", "mdm_customer")
	svc := New(r, slog.New(slog.NewTextHandler(&logs, nil)))

	base := occupyNextAutoCodes(t, r, db, 2)

	c, err := svc.Create(context.Background(), repo.CreateInput{IdempotencyKey: runKey("autocode-retry"), Name: "自动编号顺延"})
	if err != nil {
		t.Fatalf("没传 code 的 Create 不该失败，实际 %v（gRPC 码 %v）\n日志：\n%s", err, status.Code(ToStatus(err)), logs.String())
	}
	if !strings.HasPrefix(c.Code, "C") {
		t.Fatalf("自动编号应以 C 开头，实际 %q", c.Code)
	}
	n, err := strconv.ParseInt(c.Code[1:], 10, 64)
	if err != nil || n <= base+2 {
		t.Fatalf("自动编号应落在被占用的 C%06d、C%06d 之后，实际 %q", base+1, base+2, c.Code)
	}
	if c.ID != strconv.FormatInt(n, 10) {
		t.Errorf("自动编号的 id 与 code 应来自同一个序列值，实际 id=%s code=%s", c.ID, c.Code)
	}
}

// TestCreate_自动编号连续撞车耗尽重试时是Internal并记ERROR：顺延有上限。序列接下来的
// 一长串值都已被占用时，不无限重试，答 Internal（不是 AlreadyExists——调用方没传 code），
// 并记 ERROR：编号空间被显式 code 大片占用，要运维处理。
func TestCreate_自动编号连续撞车耗尽重试时是Internal并记ERROR(t *testing.T) {
	db := testDB(t)
	var logs bytes.Buffer
	r := repo.New(db, "mdm_customer_rw", "mdm_customer")
	svc := New(r, slog.New(slog.NewTextHandler(&logs, nil)))

	occupyNextAutoCodes(t, r, db, 10)

	_, err := svc.Create(context.Background(), repo.CreateInput{IdempotencyKey: runKey("autocode-exhaust"), Name: "自动编号耗尽"})
	if err == nil {
		t.Fatal("接下来 10 个自动编号都被占用时，有上限的重试应该失败")
	}
	if got := status.Code(ToStatus(err)); got != codes.Internal {
		t.Fatalf("没传 code 却撞车，应是 Internal，实际 %v：%v", got, err)
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("自动编号耗尽要运维处理，应记 ERROR，实际日志：\n%s", logs.String())
	}
}

// TestCreate_显式重复code仍是AlreadyExists：顺延只发生在自动编号上；显式传入的 code
// 被占用仍是调用方的错（409 / ALREADY_EXISTS），不记 ERROR。REST 层的 409 见
// http 包的 TestCreate_REST重复code答409。
func TestCreate_显式重复code仍是AlreadyExists(t *testing.T) {
	db := testDB(t)
	var logs bytes.Buffer
	r := repo.New(db, "mdm_customer_rw", "mdm_customer")
	svc := New(r, slog.New(slog.NewTextHandler(&logs, nil)))

	taken := runKey("C-AUTOCODE-DUP")
	if _, err := svc.Create(context.Background(), repo.CreateInput{IdempotencyKey: runKey("autocode-dup-1"), Code: taken, Name: "先来的"}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Create(context.Background(), repo.CreateInput{IdempotencyKey: runKey("autocode-dup-2"), Code: taken, Name: "后来的"})
	if got := status.Code(ToStatus(err)); got != codes.AlreadyExists {
		t.Fatalf("显式重复 code 应是 AlreadyExists，实际 %v：%v", got, err)
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("显式重复 code 是调用方的错，不该记 ERROR，实际日志：\n%s", logs.String())
	}
}
