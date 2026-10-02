package service

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/brickKit/mdm-customer/v2/backend/internal/repo"
)

// 调用方自己能纠正的错误（重复 code、找不到、版本过期）不是运维要处理的事：SDK 的
// 访问日志与 RED 指标已经记下了状态码，再记 ERROR 只会淹没真正要人处理的那几条。
// 调用方断开或进程关停时的取消也不是故障。ERROR 只留给映射成 Internal 的错误。

func bufferedService(t *testing.T) (*Service, *repo.Repo, *bytes.Buffer) {
	t.Helper()
	db := testDB(t)
	var logs bytes.Buffer
	r := repo.New(db, "mdm_customer_rw", "mdm_customer")
	return New(r, slog.New(slog.NewTextHandler(&logs, nil))), r, &logs
}

func TestWrite_调用方错误与取消不记ERROR(t *testing.T) {
	svc, r, logs := bufferedService(t)
	ctx := context.Background()

	taken := runKey("C-LOG-DUP")
	c, err := r.Create(ctx, repo.CreateInput{IdempotencyKey: runKey("svc-log"), Code: taken, Name: "日志级别测试"})
	if err != nil {
		t.Fatal(err)
	}

	// 重复 code → AlreadyExists（409）
	if _, err := svc.Create(ctx, repo.CreateInput{IdempotencyKey: runKey("svc-log-dup"), Code: taken, Name: "x"}); err == nil {
		t.Fatal("重复 code 应该报错")
	}
	// 版本过期 → Aborted（409）
	if _, err := svc.Update(ctx, UpdateInput{IdempotencyKey: runKey("svc-log-stale"), ID: c.ID, Version: c.Version + 1, Name: "x"}); err == nil {
		t.Fatal("version 过期应该报错")
	}
	if _, err := svc.SetStatus(ctx, SetStatusInput{IdempotencyKey: runKey("svc-log-stale-st"), ID: c.ID, Version: c.Version + 1, Status: "DISABLED"}); err == nil {
		t.Fatal("version 过期应该报错")
	}
	// 找不到 → NotFound（404）
	if _, err := svc.Update(ctx, UpdateInput{IdempotencyKey: runKey("svc-log-404"), ID: "999999999", Version: 1, Name: "x"}); err == nil {
		t.Fatal("不存在的客户应该报错")
	}
	if _, err := svc.AddContact(ctx, repo.AddContactInput{IdempotencyKey: runKey("svc-log-404-contact"), CustomerID: "999999999", Name: "张三"}); err == nil {
		t.Fatal("不存在的客户应该报错")
	}
	// 调用方断开 / 关停 → 取消
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.Create(cancelled, repo.CreateInput{IdempotencyKey: runKey("svc-log-cancel"), Name: "x"}); err == nil {
		t.Fatal("已取消的 ctx 应该报错")
	}

	if strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("调用方错误与取消不该记 ERROR，实际日志：\n%s", logs.String())
	}
}

// TestWrite_服务端故障仍记ERROR 是上面那条的对照组：连接池已关闭这种运维要处理的
// 故障（映射成 Internal / 500）照样记 ERROR。
func TestWrite_服务端故障仍记ERROR(t *testing.T) {
	db := testDB(t)
	var logs bytes.Buffer
	svc := New(repo.New(db, "mdm_customer_rw", "mdm_customer"), slog.New(slog.NewTextHandler(&logs, nil)))
	db.Close()

	if _, err := svc.Create(context.Background(), repo.CreateInput{IdempotencyKey: runKey("svc-log-internal"), Name: "x"}); err == nil {
		t.Fatal("连接池已关闭时应该报错")
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("服务端故障应该记 ERROR，实际日志：\n%s", logs.String())
	}
}
