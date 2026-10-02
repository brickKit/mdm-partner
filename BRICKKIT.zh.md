# mdm/customer

## 组件定位

每个客户"是谁"的唯一记录：名称、编号、税号、信用额度、状态（启用 / 停用）、联系人与开票信息。任何需要客户的组件都按 ID 从这里读，最多在本地存一份摘要副本，靠本组件的事件保持更新。

**拥有**

- 客户主数据：`code`（唯一；调用方留空时按 `C` + 6 位数字生成）、名称、税号、信用额度（十进制字符串）、状态。
- 客户的联系人（经 `AddContact` 写入，读接口还不返回）与开票信息（有表，还没有接口）。
- 事件 `mdm.customer.created.v1`、`mdm.customer.updated.v1`、`mdm.customer.disabled.v1`。

**不拥有**

- 销售上谁拥有、谁在跟进这个客户（公海、私海、负责人、跟进状态）：归 CRM 组件，它只存本组件的客户 ID 加一份摘要副本。
- 订单、发票、应收，以及在途订单占用的信用：归 `erp/sales` 与 `erp/finance`。本组件只保存它们校验时用的信用额度。
- 组织架构与"谁能看哪些行"：这里没有行级数据范围，能打开客户页面的人看到的客户主数据都一样。

## 部署前准备

- **PostgreSQL**：schema `mdm_customer`；如果你的 schema 约定会建 `mdm_customer_archive` 也可以建（本组件从不写它）。登录角色 `mdm_customer_rw`，在 `mdm_customer` 上有 `USAGE` 与 `CREATE`，以及它的密码。迁移以这个角色运行并建表，所以表归它所有——这一点有用：运行中的组件会自己给 `event_outbox` / `event_inbox` 建周分区，这需要表的所有权。brickKit 不建这些；在 BrickEnterprise 装配项目里，`make dev-env` 把密码写进 `.env`，`make db-init` 建 schema、角色与授权。
- **NATS** 在组件启动时可经 `NATS_URL` 访问：连不上时进程在启动时就退出。事件与业务变更在同一个事务里写进 Outbox 表，再由后台循环推送；运行起来之后，发送失败的事件留在 Outbox 里重试。推送走的是 core NATS：客户端重连期间发出的事件存在客户端缓冲里、按已发送记账，重连始终不成功时这些事件会丢。
- **权限**（`infra/authz`）与**身份**（`infra/iam-casdoor`，或任何提供 JWKS 的 IAM）可经 `AUTHZ_BUNDLE_URL`、`IAM_JWKS_URL` 访问，REST 路由才会返回错误以外的结果。它们是配置不是依赖：没有它们组件照样启动。
- 演示数据（可选）：组件跑起来之后，在组件目录 `make seed`，经真实 gRPC 接口建 15 个示例客户。

## 依赖说明

无。客户主数据被所有人读、自己不调任何人：别的组件经 gRPC 调它（`BatchGet` 一次取回一页里的全部客户）；它从不调用别的组件，也不消费事件，所以永远不会处在调用链中间，也不会成环。

权限 bundle 与 JWKS 从配置里的 URL 拉取，不是依赖边。没有它们时每条受保护路由都失败关闭：没有或无效的 token → `401`；没配 `IAM_JWKS_URL` → `403`；还没拉到过 bundle → `503`；合法用户但没有这个权限键 → `403`。`/healthz` 始终 `200`：它只报告本进程活着。

## 配置指南

| 变量 | 含义 |
|---|---|
| `PG_HOST` | PostgreSQL 主机。通常用项目的共享值（`$var:PG_HOST`）。 |
| `PG_PORT` | PostgreSQL 端口；默认 `5432`。 |
| `PG_DATABASE` | `mdm_customer` schema 所在的库（`$var:PG_DATABASE`）。 |
| `PG_USER` | 登录角色，字面量 `mdm_customer_rw`。进外壳时外壳以自己的角色登录，每个事务里切到这个角色（`SET LOCAL ROLE`），所以角色名必须是 `<PG_SCHEMA>_rw`。 |
| `PG_PASSWORD` | `PG_USER` 的密码。密钥：写 `${MDM_CUSTOMER_DB_PASSWORD}`（或你的密钥库引用），绝不写明文。 |
| `PG_SCHEMA` | 全部表、Outbox 与迁移状态表（`schema_migrations_mdm_customer`）所在的 schema；默认 `mdm_customer`。照样写出字面量，让每个组件的 schema 在一处看得见。 |
| `NATS_URL` | Outbox 推送事件的 NATS 地址（`$var:NATS_URL`）。 |
| `OTEL_BASE_URL` | OpenTelemetry 采集端的基础 URL；为空（默认）时什么也不导出。 |
| `AUTHZ_BUNDLE_URL` | 权限判定轮询的权限 bundle 地址，例如 `http://infra-authz-2-0-0:8223/authz/bundle`。必填：没有它每条受保护路由都回 `503`。与项目里跑的 authz 版本保持一致。 |
| `IAM_JWKS_URL` | 本地验签用户 token 的 JWKS 地址，例如 `http://infra-iam-casdoor-2-0-0:8200/.well-known/jwks.json`。必填：没有它每条受保护路由都回 `403`。 |

## 契约索引

- `contracts/mdm/customer/v1/customer.proto` —— 给别的组件的 gRPC `mdm.customer.v1.CustomerService`：`Create`、`Update`、`SetStatus`、`AddContact`（都带 `idempotency_key`；`Update` 与 `SetStatus` 还带当前 `version`，过期时返回 `ABORTED`）、`Get`、`List`（游标分页，没有 offset）、`BatchGet`（缺失的 ID 放进 `missing_ids`，不报错）、`GetSummary`（ID、编号、名称、状态、版本——摘要副本要的字段）。Go 包是独立模块 `github.com/brickKit/mdm-customer/gen/mdm/customer`。
- `contracts/customer.openapi.yaml` —— `/mdm/customer` 下的 REST，每条路由都带权限键：`GET /customers`（`mdm.customer.view`；查询参数 `q` 按 code 或名称匹配、大小写不敏感、前缀匹配在前；`status_filter`；`created_after` / `created_before`，默认最近 90 天；`cursor`、`page_size`）、`GET /customers/{id}`（`mdm.customer.view`）、`POST /customers`（`mdm.customer.create`）、`PATCH /customers/{id}`（`mdm.customer.update`）、`POST /customers/{id}/status`（`mdm.customer.set_status`；"删除"就是停用）、`POST /customers/{id}/contacts`（`mdm.customer.add_contact`）。`BatchGet` 不在 REST 上。两边的错误一致：`credit_limit` 不是非负十进制（最多 16 位整数、2 位小数，不收 `NaN`、`Inf`、科学计数法），或 `status` 不是 `ACTIVE` / `DISABLED` → `400` / `INVALID_ARGUMENT`；显式传入的 `code` 已被占用 → `409` / `ALREADY_EXISTS`；`version` 过期 → `409` / `ABORTED`；客户 ID 不存在或不是数字 → `404` / `NOT_FOUND`。
- `contracts/events/customer.events.json` —— 经 Outbox 发布的事件：`mdm.customer.created.v1`（新建）、`mdm.customer.updated.v1`（修改与重新启用）、`mdm.customer.disabled.v1`（停用）。每条都带聚合 `version`，消费者只在版本大于本地已有值时应用。不消费任何事件。
- `assembly.yaml` —— 本项目的元数据：上面五个权限键、菜单项、网关路由 `/mdm/customer/**`、schema 与角色，以及 `data_scopes: none`。

## 外壳声明

不是外壳。可以被 Go 外壳托管（BrickEnterprise 项目里是 `be/go-core`），也可以独立运行；两种形态是同一份代码。
