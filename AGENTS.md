# mdm/partner

业务伙伴组件的代码。先读 `BRICKKIT.md`；依据是装配项目 be-assembly-standard 的边界卡 `docs/zh/07-components/mdm.md`（`mdm/partner`）、协议 02（来源）、协议 07（引用的生命周期、资格与解析）、协议 14（列表）、D20–D22。

## 代码地图

| 位置 | 内容 |
| --- | --- |
| `partner/definition.go` | 组件定义：身份、依赖 mdm/org、流与事件类型、资源与操作、W |
| `partner/module.go` | 路由与系统面的注册；伙伴的新建（幂等键）、修改、状态；本版不做的路由 |
| `partner/roles.go` | 客户角色的开通与状态（`legalEntityActive` 同步问 mdm/org）；资格（`eligibility`） |
| `partner/lists.go` | 伙伴列表 |
| `partner/records.go` | 记录的类型、读取、税号重复、事件 |
| `partner/source.go` | 系统面：来源方法、资格查询、批量解析 |
| `partner/migrations/` | 表：伙伴、角色 |
| `cmd/seed/` | 种子：角色与授予、管理单元「中国区客户」与委派、伙伴、地址与联系人、各法人的客户角色（`make seed`） |
| `contract/` | 本组件发布的契约，单独的模块：`contract/proto/`、`contract/events/`、`contract/openapi.yaml` |

| 想改 | 从这里开始 | 然后 |
| --- | --- | --- |
| 资格的规则 | `roles.go` 的 `eligibility` | 集成测试的资格一段；第 7 步销售的资格取证 |
| 开通角色的前提 | `roles.go` 的 `putCustomerRole`、`legalEntityActive` | 集成测试里 mdm/org 的替身 |
| 加一种角色 | `migrations` 的 `role` 约束、`roleOf` | 契约的 `Purpose`、OpenAPI |

## 构建与测试

- `go test ./...`：清单的 `configSchema`、OpenAPI 与路由、事件载荷、合并安全。
- `go test -tags integration ./partner/`：真实的 PostgreSQL 与消息系统（`BE_SDK_TEST_PG_ADMIN`、`BE_SDK_TEST_NATS_URL`），be-sdk-go 的 `sectest` 测试部署；`mdm/org` 是按它发布的契约写的替身，不导入它的代码。
- `brickkit lint`

## 设计取舍

- **新建伙伴用幂等键**：伙伴没有天然的唯一键（税号可能没有，也可能重复），重试安全只能靠请求带的键。
- **问 `mdm/org` 在事务之外**：先取证、再在事务里写；事务里不发网络调用。法人在两步之间被暂停的窗口由资格查询与执行环节的检查兜住（协议 07）。
- **伙伴与角色分开记录、分开发事件**：销售只关心某法人下的客户角色，投影角色就够了，不必随联系人变化重算。

## 易错点

- `mdm/org` 不可达不能当成“法人不存在”：返回依赖不可用，而不是 `LEGAL_ENTITY_NOT_USABLE`。
- 资格是“此刻”的证据：调用方要在 W 之内提交，不要缓存回答。

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
