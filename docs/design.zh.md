[English](design.md) · [中文](design.zh.md)

# mdm/customer 设计

只写结论，给要改本组件设计的人。怎么用：`BRICKKIT.zh.md`；代码怎么布局：`AGENTS.zh.md`。

## 边界

本组件是"客户是谁"的唯一记录：名称、编号、税号、信用额度、状态、联系人、开票信息。

| 不在这里 | 归谁 | 为什么 |
|---|---|---|
| 销售上谁拥有、谁在跟进客户（公海私海、负责人、跟进状态、360° 视图） | CRM 组件 | 那是"销售怎么跟客户打交道"，不是"客户是谁"；CRM 存本组件的 ID 加一份摘要副本 |
| 订单、发票、应收、在途订单占用的信用 | `erp/sales`、`erp/finance` | 交易发生在那里；本组件只保存它们校验时用的信用额度 |
| 组织架构 | 组织主数据组件 | 无关：客户主数据没有行级范围（见数据范围） |

删除客户就是停用（`POST /customers/{id}/status`）；没有删除端点，因为订单和发票还会一直引用这个客户。

## 拥有的数据

| 表 | 分区 | 说明 |
|---|---|---|
| `customers` | 否 | `code` 唯一（`customers_code_uniq`）；`credit_limit NUMERIC(18,2)`；`status` 为 `ACTIVE` / `DISABLED`；`version` 用于乐观锁 |
| `contacts` | 否 | 外键指向 `customers` |
| `billing_infos` | 否 | 外键指向 `customers`；还没有任何接口读写它 |
| `command_idempotency` | 否 | 主键 `idempotency_key`，记第一次执行的结果 ID |
| `event_outbox`、`event_inbox` | 按 `created_at` 周分区 | 推送循环从 Outbox 读；分区由组件自己提前 4 周建好 |

没有终态：`DISABLED` 表示"不能用于新业务"，客户可以重新启用。调用方留空 `code` 时用 ID 序列生成 `C` + 6 位数字，否则按传入值使用（导入、从别的系统迁移）。

## 契约面

给别的组件的 gRPC `mdm.customer.v1.CustomerService`：

| rpc | 类型 | 说明 |
|---|---|---|
| `Create` | 写 | 按 `idempotency_key` 幂等 |
| `Update`、`SetStatus` | 写 | 幂等，并带当前 `version`；版本过期 → `ABORTED` |
| `AddContact` | 写 | 幂等；不发事件 |
| `Get`、`List` | 读 | `List` 按游标分页，没有 offset；默认窗口最近 90 天 |
| `BatchGet` | 读 | 调用方用它代替循环调 `Get`；缺失的 ID 放进 `missing_ids` |
| `GetSummary` | 读 | ID、编号、名称、状态、版本：摘要副本存的字段 |

`/mdm/customer` 下的 REST，每条路由都带权限键：`GET /customers` 与 `GET /customers/{id}`（`mdm.customer.view`）、`POST /customers`（`mdm.customer.create`）、`PATCH /customers/{id}`（`mdm.customer.update`）、`POST /customers/{id}/status`（`mdm.customer.set_status`）、`POST /customers/{id}/contacts`（`mdm.customer.add_contact`）。`BatchGet` 只在 gRPC 上：没有哪个终端页面会按一串客户 ID 查询，放到公网 API 上只会被当成批量导出用。没有给超时写调用确认结果的状态端点：没有组件在自己的事务里同步写本组件。

REST 与 gRPC 的错误一致（be-sdk-go 把 gRPC 码映射成 HTTP 状态码）。`credit_limit` 按 `NUMERIC(18,2)` 的形状校验字符串：非负、最多 16 位整数、2 位小数，不收 `NaN`、`Inf`、科学计数法；按浮点解析会放过 `NaN`，而 `NUMERIC` 里的 `NaN` 排在所有数之上，`erp/sales` 拿它做的信用检查永远通过。`credit_limit` 写法不对或 `status` 不是 `ACTIVE` / `DISABLED` → `400` / `INVALID_ARGUMENT`；显式传入的 `code` 已被占用 → `409` / `ALREADY_EXISTS`；`version` 过期 → `409` / `ABORTED`；ID 不存在或不是数字 → `404` / `NOT_FOUND`。调用方错误记 Info，从不记 ERROR。

**关键字搜索**（`GET /customers?q=`），给客户选择器用（建单、商机建档、移动端搜索）：

- `q` 去掉首尾空白；为空就是不过滤，结果与不带 `q` 的列表完全一样。
- `lower(code)` 或 `lower(name)` 包含 `lower(q)` 即命中。匹配用 `strpos` 与 `starts_with`，不用 `LIKE`：用户输入里的 `%` 与 `_` 是字面字符，不用转义。
- code 或名称以 `q` 开头的档位为 0，只是包含的为 1。排序：档位，再 `created_at` 倒序，再 `id` 倒序。
- 档位是 keyset 的一部分：翻到档位 1 之后游标带上它，分界处翻页不漏不重。不带 `q` 的列表游标编码 `created_at` 与 `id`；带档位的页在前面加上档位。
- 默认 90 天窗口照常生效，与项目里所有列表一致；必须找到更早客户的选择器显式给 `created_after`。REST 读 `created_after` / `created_before`（RFC 3339；格式不对 → `400`）。
- 不加索引。包含匹配不管有什么索引都要扫描窗口内的行，前缀索引也服务不了与它 `OR` 在一起的条件；表是主数据量级（几千到几万行）。真要变大，答案是 trigram 索引（`pg_trgm`），不是前缀索引。
- 只在 REST 上。gRPC `ListRequest` 不变；哪个调用方需要时再以只增的方式加 `q`。

## 事件

发布，全部经 Outbox、与写操作同一个事务：

| subject | 何时 | payload |
|---|---|---|
| `mdm.customer.created.v1` | `Create` | id、编号、名称、税号、信用额度、状态、版本 |
| `mdm.customer.updated.v1` | `Update`，以及 `SetStatus` 回到 `ACTIVE` | id、名称、税号、信用额度、状态、版本 |
| `mdm.customer.disabled.v1` | `SetStatus` 到 `DISABLED` | id、版本（另带与 updated 相同的字段） |

没有 "enabled" 这个 subject：重新启用就是一次 payload 带状态的更新。新增 subject 是契约变更，先改契约。消费者只在事件 `version` 大于本地已有值时应用，因此不怕乱序与重复投递。

消费：无。消费一个事件，主数据就依赖了别人。

## 依赖

无，这是设计本身。预期中但没有的：

| 不是依赖 | 为什么 |
|---|---|
| 任何业务组件 | 主数据被所有人读、自己不调任何人 |
| `infra/iam-casdoor` | token 用 `IAM_JWKS_URL` 的 JWKS 本地验签：是配置，不是边 |
| `infra/authz` | 权限 bundle 从 `AUTHZ_BUNDLE_URL` 轮询：是配置，不是边 |
| NATS | 经 `NATS_URL` 访问的基础资源，不是组件 |

## 在同步调用图里的位置

叶子：只有入边。`crm/opportunity`、`erp/sales` 与移动端 BFF 经 gRPC 调它；它不调任何人。它是项目三个枢纽之一（主数据被所有人读；`erp/inventory` 是库存移动的唯一写入方；`erp/finance` 多半被命令、听事件）。同时被 CRM 与 ERP 读，并不违反"CRM 与 ERP 之间没有同步边"：本组件两者都不是。

## 分区与归档

`customers`、`contacts`、`billing_infos` 不分区、不归档：主数据不随时间增长，没有终态也就没有"这一行已经结束"的时刻。`mdm_customer_archive` schema 可能按项目约定存在；没有东西往里写，`BatchGet` 的归档回退查不到表，只返回热表的行。`event_outbox` / `event_inbox` 按周分区，随分区自然滚动。

## 数据范围

无（`data_scopes: none`）。客户主数据是销售、财务、仓库都要看到同一份的参照数据；"只看我的客户"是对 CRM 关系的限制，不是对主记录的限制。所以表上没有负责人或部门列。

## 参考实现

| 项目 | 版本 | 看的模块 | 借鉴了什么 | 许可证 | 用法 |
|---|---|---|---|---|---|
| Odoo | 17.0 | `addons/base/models/res_partner.py` | 客户与联系人哪些字段重要（税号、信用相关字段） | LGPL-3 | 借鉴思路 |
| Apache OFBiz | 18.12 | `applications/party/entitydef/entitymodel.xml` | 当事人与其角色、关系分开：支持把 CRM 关系留在主记录之外 | Apache-2.0 | 借鉴思路 |

**明确避开的**：Odoo 用一张 `res.partner` 表装公司、个人、地址、银行账户，靠 `is_company` / `parent_id` 区分。这里按数据所有权拆开（`customers`、`contacts`、`billing_infos`），没有哪条查询需要先判断"这一行到底是什么"。

没有槽位族：两个参考指向同一个方向，没有哪类客户需要不同的形状。

## 未决问题

| 问题 | 目前的答案 |
|---|---|
| 信用额度用 `NUMERIC(18,2)` 够不够、什么币种？ | 单币种、两位小数，与订单金额一致；以后放宽是只增的迁移 |
| 联系人写得进去（`AddContact`），但 `Get` / `List` / `BatchGet` 不返回；开票信息有表，还没有读写接口 | 还没有页面需要；补上是只增的契约变更 |
| 关键字搜索要不要无视默认 90 天窗口？ | 暂时不：项目约定对所有列表生效，选择器传 `created_after`。选择器用起来别扭时再议（由前端工作决定） |
| 同一个 `idempotency_key` 的两个并发请求：输的那个收到错误，而不是赢家的结果 | 只落一条，这正是承诺的内容；要让输家也拿到赢家的结果，需要在主键冲突时重试一次 |
