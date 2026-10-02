package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	customerv1 "github.com/brickKit/mdm-customer/gen/mdm/customer/v1"

	"github.com/brickKit/mdm-customer/v2/backend/internal/repo"
	"github.com/brickKit/mdm-customer/v2/backend/internal/service"
)

// 这里测的是 gRPC 面交出的状态码：真的 grpc.Server（内存里的 bufconn 连接，
// 请求照样序列化、过网络栈），真的测试库。

var keySeq int64

// runKey 返回本次运行独有的幂等键 / 客户编号（测试库跨运行保留数据）。
func runKey(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), atomic.AddInt64(&keySeq, 1))
}

func testClient(t *testing.T) (customerv1.CustomerServiceClient, *repo.Repo) {
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
	r := repo.New(db, "mdm_customer_rw", "mdm_customer")

	lis := bufconn.Listen(1 << 20)
	gs := grpclib.NewServer()
	customerv1.RegisterCustomerServiceServer(gs, New(service.New(r, slog.Default())))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpclib.NewClient("passthrough:///bufnet",
		grpclib.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpclib.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return customerv1.NewCustomerServiceClient(conn), r
}

// TestCreditLimit_gRPC的NaN_Inf_科学计数法等写法是InvalidArgument：别的组件经
// gRPC 写客户时，credit_limit 不是严格的非负十进制就拿到 InvalidArgument，库里的
// 额度与 version 不变（NaN 一旦落库就随事件流进 erp/sales 与 erp/finance）。
func TestCreditLimit_gRPC的NaN_Inf_科学计数法等写法是InvalidArgument(t *testing.T) {
	cli, r := testClient(t)
	ctx := context.Background()

	base, err := r.Create(ctx, repo.CreateInput{
		IdempotencyKey: runKey("grpc-credit-base"), Code: runKey("C-GRPC-CREDIT"), Name: "额度校验基线", CreditLimit: "100.00"})
	if err != nil {
		t.Fatal(err)
	}

	for _, v := range []string{"NaN", "Inf", "1e3", "-1", "1.234", "abc"} {
		_, err := cli.Create(ctx, &customerv1.CreateRequest{
			IdempotencyKey: runKey("grpc-credit-bad"), Name: "额度校验", CreditLimit: v})
		if got := status.Code(err); got != codes.InvalidArgument {
			t.Errorf("Create credit_limit=%q 应该是 InvalidArgument，实际 %v（%v）", v, got, err)
		}
		_, err = cli.Update(ctx, &customerv1.UpdateRequest{
			IdempotencyKey: runKey("grpc-credit-bad-upd"), Id: base.ID, Version: base.Version,
			Name: base.Name, CreditLimit: v})
		if got := status.Code(err); got != codes.InvalidArgument {
			t.Errorf("Update credit_limit=%q 应该是 InvalidArgument，实际 %v（%v）", v, got, err)
		}
	}

	got, err := cli.Get(ctx, &customerv1.GetRequest{Id: base.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.CreditLimit != "100.00" || got.Version != base.Version {
		t.Fatalf("被拒绝的 Update 不该改库，期望 100.00 / version %d，实际 %s / %d", base.Version, got.CreditLimit, got.Version)
	}
}

// TestCreate_gRPC重复code是AlreadyExists_不存在的客户是NotFound：别的组件据状态码
// 区分"编号被占"（AlreadyExists）、"客户不存在"（NotFound）与"版本过期"（Aborted）。
func TestCreate_gRPC重复code是AlreadyExists_不存在的客户是NotFound(t *testing.T) {
	cli, r := testClient(t)
	ctx := context.Background()

	taken := runKey("C-GRPC-DUP")
	c, err := r.Create(ctx, repo.CreateInput{IdempotencyKey: runKey("grpc-dup-first"), Code: taken, Name: "先来的客户"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cli.Create(ctx, &customerv1.CreateRequest{IdempotencyKey: runKey("grpc-dup-second"), Code: taken, Name: "后来的客户"})
	if got := status.Code(err); got != codes.AlreadyExists {
		t.Errorf("重复 code 应该是 AlreadyExists，实际 %v（%v）", got, err)
	}

	_, err = cli.Update(ctx, &customerv1.UpdateRequest{IdempotencyKey: runKey("grpc-404-upd"), Id: "999999999", Version: 1, Name: "x"})
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("Update 不存在的客户应该是 NotFound，实际 %v（%v）", got, err)
	}
	_, err = cli.Update(ctx, &customerv1.UpdateRequest{IdempotencyKey: runKey("grpc-stale-upd"), Id: c.ID, Version: c.Version + 1, Name: "x"})
	if got := status.Code(err); got != codes.Aborted {
		t.Errorf("Update 带过期 version 应该是 Aborted，实际 %v（%v）", got, err)
	}
}

// TestSetStatus_gRPC漏填status是InvalidArgument：CUSTOMER_STATUS_UNSPECIFIED（proto3
// 里不填就是它）不能被当成 ACTIVE——一个想停用客户、却漏填了字段的调用方，会把客户
// 悄悄启用。库里的状态与 version 不变。
func TestSetStatus_gRPC漏填status是InvalidArgument(t *testing.T) {
	cli, r := testClient(t)
	ctx := context.Background()

	c, err := r.Create(ctx, repo.CreateInput{IdempotencyKey: runKey("grpc-unspec"), Code: runKey("C-GRPC-UNSPEC"), Name: "漏填状态测试"})
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := cli.SetStatus(ctx, &customerv1.SetStatusRequest{
		IdempotencyKey: runKey("grpc-unspec-disable"), Id: c.ID, Version: c.Version,
		Status: customerv1.CustomerStatus_CUSTOMER_STATUS_DISABLED})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.SetStatus(ctx, &customerv1.SetStatusRequest{
		IdempotencyKey: runKey("grpc-unspec-set"), Id: c.ID, Version: disabled.Customer.Version})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("漏填 status 应该是 InvalidArgument，实际 %v（%v）", got, err)
	}
	got, err := cli.Get(ctx, &customerv1.GetRequest{Id: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != customerv1.CustomerStatus_CUSTOMER_STATUS_DISABLED || got.Version != disabled.Customer.Version {
		t.Fatalf("漏填 status 不该改库，实际 %v / version %d", got.Status, got.Version)
	}
}
