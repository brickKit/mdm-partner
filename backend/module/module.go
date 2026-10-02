// Package module 是 mdm-customer 唯一的装配入口。独立运行（cmd/server 的
// besdk.RunStandalone）与进外壳走同一个 New：模块只交回零件（HTTP handler、
// gRPC 注册函数、后台循环），谁去 Listen、谁开连接池、谁初始化 OTel 与信号
// 处理，全归调用方——这样同一份代码进外壳之后不会与别的成员互相覆盖。
//
// 这个组件是被所有人读的主数据 + 常规 CRUD，repo / service / http / grpc
// 四层直给，不用设计模式。
package module

import (
	"context"

	besdk "github.com/brickKit/be-sdk-go"
	customerv1 "github.com/brickKit/mdm-customer/gen/mdm/customer/v1"
	"google.golang.org/grpc"

	grpcapi "github.com/brickKit/mdm-customer/v2/backend/internal/grpc"
	httpapi "github.com/brickKit/mdm-customer/v2/backend/internal/http"
	"github.com/brickKit/mdm-customer/v2/backend/internal/partition"
	"github.com/brickKit/mdm-customer/v2/backend/internal/repo"
	"github.com/brickKit/mdm-customer/v2/backend/internal/service"
)

// New 构造 mdm-customer 模块。签名是外壳与 RunStandalone 共同依赖的约定，
// 不改。
func New(ctx context.Context, rt *besdk.Runtime) (*besdk.Module, error) {
	// 配置只从 rt.Config 读，模块里不碰 os.Getenv：一个进程只有一份环境，
	// 进外壳后各成员的 PG_SCHEMA 会互相覆盖，不报错，模块就按别人的 schema
	// 读写数据。
	schema := rt.Config.StringOr("PG_SCHEMA", "mdm_customer")
	// role 是每个事务里 SET LOCAL ROLE 的目标：独立运行时它就是登录角色；
	// 进外壳后外壳以自己的角色登录，靠这一步切到本组件的角色。
	role := schema + "_rw"

	// 连接池、日志、Tracer、Meter 都从 rt 来，不自己 sql.Open、不自己
	// SetTracerProvider：进程级的初始化"最后一个赢"，进外壳后症状是一路全绿、
	// trace 却都挂在别人名下。
	r := repo.New(rt.DB, role, schema)
	svc := service.New(r, rt.Logger)

	// engine 必须用 besdk.NewGinEngine：它挂好了 request-id、trace、RED 指标、
	// 访问日志、panic 恢复、错误到状态码的映射与 /healthz、/metrics。
	eng := besdk.NewGinEngine(rt)
	httpapi.RegisterRoutes(eng, svc)

	return &besdk.Module{
		HTTPHandler: eng,

		// gRPC 由调用方在 extraPorts 的 grpc 端口上 Listen。进外壳后同进程的
		// 别的成员照样经 gRPC 调本组件，不直接调函数：边界在合并时不消失，
		// 组件才能随时拆回独立部署。
		RegisterGRPC: func(gs *grpc.Server) {
			customerv1.RegisterCustomerServiceServer(gs, grpcapi.New(svc))
		},

		// 后台循环：Outbox 推送 + event_outbox / event_inbox 的周分区维护（分区
		// 不存在时写入会失败）。两个循环必须并发跑：StartOutboxPump 阻塞到 ctx
		// 取消才返回，顺序调用的话第二个永远轮不到。
		Start: func(ctx context.Context) error {
			errCh := make(chan error, 2)
			go func() { errCh <- besdk.StartOutboxPump(ctx, rt.DB, schema, rt.NATS, rt.Logger) }()
			go func() { errCh <- partition.Start(ctx, rt.DB, role, schema, rt.Logger) }()

			select {
			case <-ctx.Done():
				return nil
			case err := <-errCh:
				return err // 返回 error，不 log.Fatal：进外壳后一个成员退出进程，所有成员一起没了
			}
		},
		Stop: func(ctx context.Context) error { return nil }, // 后台循环靠 ctx 退出
	}, nil
}
