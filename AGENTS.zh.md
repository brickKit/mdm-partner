[English](AGENTS.md) · [中文](AGENTS.zh.md)

# mdm/customer

开发本组件的 AI 指南。用法、边界与契约：BRICKKIT.md；为什么长这样：`docs/design.zh.md`；依赖、配置与部署：component.yaml。

## 代码地图

| 路径 | 负责 |
|---|---|
| `backend/module/module.go` | 唯一入口 `New(ctx, rt)`：组装 repo → service → HTTP + gRPC，启动 Outbox 推送与分区循环。独立运行与进外壳是同一个函数 |
| `backend/cmd/server/main.go` | 一行 `besdk.RunStandalone(module.New)` |
| `backend/cmd/migrate/main.go` | 一行 `migrate.Main(migrations.FS)`：迁移容器的入口（`./migrate up`） |
| `backend/internal/repo/repo.go` | 共用类型、哨兵错误（`ErrVersionConflict`、`ErrNotFound`、`ErrInvalidCursor`）、列清单与扫描函数 |
| `backend/internal/repo/write.go` | 四个写命令；每个都在一个事务里做 查幂等键 → 写 → 记幂等键 → Outbox |
| `backend/internal/repo/read.go` | `BatchGet`、`List`（关键字 `q`、排序档位、keyset 分页）、拼 SQL 的 `listSQL` |
| `backend/internal/repo/cursor.go` | 列表游标：`created_at` 与 `id` 的 base64，翻到"只是包含"那一档后再带上档位 |
| `backend/internal/service/` | 入参校验与错误 → gRPC 状态码映射（`status.go`），REST 与 gRPC 共用 |
| `backend/internal/http/http.go` | REST 路由，每条都带权限键注册；`parseListInput` |
| `backend/internal/grpc/grpc.go` | `mdm.customer.v1.CustomerService` |
| `backend/internal/partition/` | 后台循环：给 `event_outbox` / `event_inbox` 提前 4 周建周分区 |
| `migrations/` | SQL 迁移，由 `migrations/embed.go` 嵌进二进制 |
| `contracts/` | proto、OpenAPI、事件 schema |
| `gen/mdm/customer/` | 生成的 Go 代码：独立的嵌套 Go 模块，单独打 tag（gen/mdm/customer/v1.x.y）；不手改 |
| `scripts/` | 本地演示数据 `seed.sh` / `seed-clean.sh` |

| 要做的事 | 从这里开始 | 然后 |
|---|---|---|
| 列表的关键字搜索 `q` | `backend/internal/repo/read.go`（`listSQL`） | `backend/internal/repo/cursor.go`、`backend/internal/repo/search_test.go` |
| 新增一个 REST 查询参数 | `backend/internal/http/http.go`（`parseListInput`） | `contracts/customer.openapi.yaml`、`backend/internal/http/http_test.go` |
| 客户加一个字段 | `migrations/`（新迁移） | `contracts/`（只增）、`buf generate`、`backend/internal/repo/repo.go`（`customerColumns`） |
| 新增一个写命令 | `backend/internal/repo/write.go` | `backend/internal/service/service.go`，再接 REST 与 gRPC |
| 某个错误回了不对的状态码 | `backend/internal/service/status.go` | `backend/internal/repo/repo.go` 里的哨兵错误 |

## 构建与测试

```bash
# 测试连测试库 brickkit_test_db，绝不连 brickkit_db（项目根 make test-db-init 准备）
set -a; . ../../../.env; set +a
export TEST_PG_DSN="postgres://postgres:${POSTGRES_PASSWORD}@localhost:5432/brickkit_test_db?sslmode=disable"
make test                    # 每个包都是 "ok"；没设 TEST_PG_DSN 直接失败
go test ./... -count=1 -v | grep -c -- '--- SKIP'   # 0：没有测试被跳过
make check-version dag-check contract-check import-scan module-check   # 每个打印一行 ✓
make docs-check              # "0 with errors, 0 warnings"
# 迁移，以登录角色运行：
PG_HOST=localhost PG_PORT=5432 PG_DATABASE=brickkit_test_db PG_USER=mdm_customer_rw \
  PG_PASSWORD="$MDM_CUSTOMER_DB_PASSWORD" PG_SCHEMA=mdm_customer make migrate-idempotent   # ✓ 迁移幂等
```

`buf generate` 之后（契约变了），契约包要打新 tag：见易错点。真机在项目根跑 `make verify ID=mdm/customer ROUTE=/mdm/customer/customers FOCUS=1`：构建镜像、只起本组件需要的东西、核对迁移 / 健康 / 权限判定、跑一次 focus、收尾。

## 设计取舍

- **被所有人读，自己不调任何人。** 没有依赖、不消费事件：主数据在调用图里永远是叶子。调用方用一次 `BatchGet` 取回一页里的全部客户，不循环调 `Get`。
- **没有行级数据范围**（`data_scopes: none`）：客户主数据是全员共用的参照数据。谁在跟哪个客户打交道是 CRM 的数据，不是本组件的。
- **每个写都幂等、带版本。** `idempotency_key` 是 `command_idempotency` 的主键，与写操作在同一个事务里记录；`Update` / `SetStatus` 带当前 `version`（乐观锁）。停用不是终态：客户可以重新启用，所以什么都不归档。
- **搜索排序在 SQL 里**：`match_rank`（0 = code 或名称以 `q` 开头，1 = 只是包含）在内层查询里算，并且是 keyset 的一部分；用 `strpos` / `starts_with` 而不是 `LIKE`，用户输入不用转义。不加索引：包含匹配本来就要扫描窗口内的行，表是主数据量级。
- **默认 90 天窗口对搜索同样生效**，与所有列表一致（项目约定）；要找更早建档的客户，调用方显式给 `created_after`。
- **repo 按读写拆开**（`write.go`、`read.go`）：四个写共用同一个事务形状（`replayCustomer`、`recordIdempotency`、`publish`、`updateWithVersion`）。

## 易错点

| 不许 | 症状 | 原因 |
|---|---|---|
| 给 `dependencies.components` 加一条，或消费某个事件 | 编译、测试全绿；组件不再是叶子，下一个人再加一条边就可能成环 | 主数据被所有人读；`make dag-check` 遇到任何依赖都失败 |
| 删掉 `BatchGet`，或让调用方循环调 `Get` | 测试照样绿；N 行的列表页发 N 次 gRPC | `BatchGet` 是所有调用方依赖的批量读法 |
| 回填调用方传来的 `credit_limit`，不从库里读回 | `Create` 回 `"0"`，随后 `Get` 同一行回 `"0.00"` | `NUMERIC(18,2)` 会规整数值；`insertCustomer` 用 `RETURNING` 读回 |
| 给 `customers` / `contacts` / `billing_infos` 分区，或加负责人 / 部门列 | 迁移能过；这些列永远没人用，分区表想撤销就是一次数据迁移 | 主数据，几千行；没有数据范围 |
| 改了 `contracts/mdm/customer/v1/customer.proto` 的注释或选项，却不给契约包打 tag | 本地全绿（`replace` 掩盖了它）；外壳拉 `mdm-customer/v2` 时对着 `go.mod` require 的那个 tag 里的旧 `gen/` 编译 | `gen/mdm/customer/` 下有任何变化都要打新 tag `gen/mdm/customer/v1.x.y`，根 `go.mod` require 它 |
| 列表不按 `match_rank, created_at DESC, id DESC` 排序，或游标里丢了档位 | 翻页恰好在"前缀匹配结束、包含匹配开始"的地方漏行或重复 | `listSQL` 的 keyset 条件依赖这个顺序；`TestList_q与游标一起翻页不重不漏` 守着 |
| 对原始输入用 `LIKE` / `ILIKE` 搜索 | `q=%` 返回全部客户 | `strpos` / `starts_with` 把 `%` 与 `_` 当字面字符 |

## 改代码前自查

1. 这件事关于"客户是谁"（这里），还是"谁在跟这个客户打交道"（CRM）？后者绝不放这里。
2. 我在加依赖、加消费的事件、加分区或数据范围列吗？停下：看易错点与 `docs/design.zh.md`。
3. 改契约？只增：新字段、新 rpc、新查询参数。绝不删或改类型：字段、rpc、路径、事件 subject 都一样。
4. `gen/` 变了吗？那契约包要打新 tag，`go.mod` 要 require 它。
5. 新 REST 路由用 `besdk.GET` / `POST` / `PATCH` 加 `assembly.yaml` 里的权限键注册。
6. 新规则 → 先写会失败的测试（涉及 SQL 的用真库），再写代码。
7. 发布之后第一次改动前先升 `metadata.version`；BRICKKIT.md 与 `docs/design.md` 与代码同一个提交更新。
