# mdm/partner

业务伙伴组件的代码。用法与边界在 `BRICKKIT.md`，为什么这样设计（术语、数据、状态与规则、未决问题）在 [docs/design.md](docs/design.md)；跨组件的规则在装配项目 be-assembly-standard 的协议 02（来源）、协议 07（引用的生命周期、资格与解析）、协议 14（列表）与 D20–D22。

## 代码地图

代码按业务模块分层。**先读这一节，再进目录**：每个代码目录都有自己的 `AGENTS.md`（同目录的 `CLAUDE.md` 让 Claude Code 读这个目录的文件时自动带上它），写这一层负责什么、不负责什么、每个文件、依赖谁、测在哪。

| 目录 | 是什么 |
| --- | --- |
| [`partner/`](partner/AGENTS.md) | 入口：组件定义与组装（路由、系统面、恢复核对都在这里登记，转给业务模块）；不写业务。`main.go` 与外壳只引用它 |
| [`internal/`](internal/AGENTS.md) | 业务：下面的模块各一个包；模块之间的依赖表在它的 `AGENTS.md` |
| [`itest/`](itest/AGENTS.md) | 集成测试，按业务流程分文件；它的 `AGENTS.md` 是测试索引 |
| `partner/migrations/` | 表：伙伴、角色 |
| `cmd/seed/` | 种子：角色与授予、管理单元「中国区客户」与委派、伙伴、地址与联系人、各法人的客户角色（`make seed`） |
| `contract/` | 本组件发布的契约，单独的模块：`contract/proto/`、`contract/events/`、`contract/openapi.yaml`；事件 schema 同时是来源方法的 `data` 与 OpenAPI 的形状 |
| `docs/design.md` | 设计：术语、表、状态与规则、命令、事件、依赖、实现状态、未决问题 |

业务模块（`internal/` 下）：

| 模块 | 负责 | 拥有的表 |
| --- | --- | --- |
| [`partners`](internal/partners/AGENTS.md) | 伙伴：新建（幂等键）、修改、状态、列表；税号的规范化与查重 | `partner` |
| [`roles`](internal/roles/AGENTS.md) | 按法人开通的客户、供应商角色与它们的状态；资格（新单据此刻能不能引用这个伙伴） | `partner_role` |
| [`source`](internal/source/AGENTS.md) | 系统面：来源方法、资格查询、批量解析 | — |

共用的：[`core`](internal/core/AGENTS.md)（名字与资源定义、校验、审计与事件的写法、记录的类型与读取、税号查重的查询、本版不做的路由）。

| 想改 | 从这里开始 | 然后 |
| --- | --- | --- |
| 资格的规则 | `internal/roles` 的 `EligibilityOf` | `itest/eligibility_integration_test.go`；第 7 步销售的资格取证 |
| 开通角色的前提 | `internal/roles` 的 `putRole`、`legalEntityActive` | `itest/roles_integration_test.go`（组织组件 mdm/org 的替身） |
| 加一种角色 | `migrations` 的 `role` 约束（3.0.39 加供应商的 `0002`）、`internal/roles` 的 `roleOf` 与两个路由的包装 | 契约的 `Purpose`、OpenAPI |
| 税号查重或规范化 | `internal/core/records.go` 的 `Duplicates`、`internal/partners` 的 `partnerIn.apply` | `itest/partners_integration_test.go` |
| 加字段、加接口、改事件 | 装配项目 be-assembly-standard 规范里的“改动配方” | `docs/design.md` 的数据与命令 |

## 构建与测试

- `go test ./...`（`make test`）：清单的 `configSchema` 与 `events`、OpenAPI 与路由、事件载荷、合并安全。
- `make test-integration`（`go test -tags integration ./itest/`）：真实的 PostgreSQL 与消息系统；每个文件测哪条流程、要什么前提，在 [itest/AGENTS.md](itest/AGENTS.md)。
- `brickkit lint`

## 设计取舍

- **新建伙伴用幂等键**：伙伴没有天然的唯一键（税号可能没有，也可能重复），重试安全只能靠请求带的键。
- **问 `mdm/org` 在事务之外**：先取证、再在事务里写；事务里不发网络调用。法人在两步之间被暂停的窗口由资格查询与执行环节的检查兜住（协议 07）。
- **伙伴与角色分开记录、分开发事件**：销售只关心某法人下的客户角色，投影角色就够了，不必随联系人变化重算。

## 易错点

- `mdm/org` 不可达不能当成“法人不存在”：返回依赖不可用，而不是 `LEGAL_ENTITY_NOT_USABLE`。
- 资格是“此刻”的证据：调用方要在 W 之内提交，不要缓存回答。
- 资格查询不看法人的状态：法人暂停后，已开通的角色仍答 `eligible`；挡住新单据的是引用方向 `mdm/org` 取的法人证据（`docs/design.md` 的未决问题）。

## 改代码前自查

- 改了契约（`contract/`）：proto 改后 `make gen`，发布 `contract/v…`，再改这里的依赖；OpenAPI 与路由由测试核对。
- 改了版本号吗？每次改动都升版本。

<!-- brickkit:managed:begin lang=zh -->
<!-- 由 brickkit 维护（init、add、remove、upgrade、skills update）：这对标记之间的改动会被覆盖 -->

## BrickKit

这是一个 BrickKit 组件：平台只读 `component.yaml`。它依赖的规则：

- `configSchema` 的键就是代码读的环境变量名。不能用保留名：`COMPONENT_ID`、`COMPONENT_VERSION`、`PORT`、`BRICKKIT_SERVED_MEMBERS`、`BRICKKIT_SERVED_MEMBERS_CONFIG`，以及任何 `*_ENDPOINT`。
- 依赖写精确版本。依赖的地址以 `<ID>_ENDPOINT` 注入；缺席的可选依赖根本没有这个变量，读的时候要带兜底。
- `/healthz` 只查本进程，不查依赖。迁移命令用同一个镜像跑，遇到不认识的参数必须直接失败；`local.migrationCommand` 从源码跑同一条迁移，给 `mode: local` 用。
- `BRICKKIT.md` 会随版本进入每个使用它的项目，在那里是脱离仓库单独读的：跟代码一起改，不放相对链接。
- 发版：改 `metadata.version`，提交、推送，`brickkit release`。`brickkit lint` 会检查清单和这些文档——在项目里、在这个目录下跑，只查这个组件（`--all` 查整个项目）。
- 完整规则在 `brickkit-component` 技能里（装了技能的项目或仓库根目录下的 `.claude/skills/brickkit-component/SKILL.md`；`brickkit skills update` 会装上）；参数问 `brickkit <命令> --help`；BrickKit 自己的文档用 `brickkit docs` 看。
<!-- brickkit:managed:end -->
