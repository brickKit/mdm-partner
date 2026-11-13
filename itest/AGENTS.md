# itest/：集成测试与测试索引

业务伙伴组件对着真实的 PostgreSQL 与消息系统、经它真实的接口（用户面带 be-sdk-go `sectest` 的令牌，系统面走 mTLS，发件表）的测试（L2）。`mdm/org` 是按它发布的契约写的替身（引入它的契约模块，不是代码），可以中途停掉。构建标签 `integration`；一条业务流程一个文件。

## 怎样跑

- `make test-integration`（`go test -count=1 -tags integration ./itest/`），约一秒。
- 环境变量：`BE_SDK_TEST_PG_ADMIN=postgres://postgres:<口令>@localhost:5432/brickkit_test_db`（口令是装配项目 `.env` 的 `POSTGRES_PASSWORD`）、`BE_SDK_TEST_NATS_URL=nats://localhost:4222`。
- 前提：装配项目里 `make up` 起了 `be-postgres` 与 `be-nats`；`be-postgres` 里有库 `brickkit_test_db`（没有就 `docker exec be-postgres createdb -U postgres brickkit_test_db`）；测试每次开始时删掉再建自己的 schema `mdm_partner_it`，跑完留着；端口 28225、9225 与替身的 19093 要空着，被占时测试直接失败。
- **只有一个测试** `TestPartnerEndToEnd`：按下表的顺序跑各步（普通的方法调用，不是子测试），后面的步用前面留下的伙伴与角色，所以一步不能单独跑。

## 文件

按运行顺序：

| 文件 | 步 | 测的流程 | 被测的模块 |
| --- | --- | --- | --- |
| `partners_integration_test.go` | `partners`、`partnerEdits` | 新建伙伴一个幂等键一次，没有键被拒；同国家同税号提示、不拒绝（D22）；过期的版本被拒、关键字找拼音首字母、没有授予看不见、合并本版不做 | `partners`、`core` |
| `roles_integration_test.go` | `roles` | 按法人开通客户角色；法人暂停或不存在被拒；`mdm/org` 不可达时角色不开通、原因不是“法人不可用”（协议 07） | `roles` |
| `eligibility_integration_test.go` | `eligibility` | 没开角色的法人不能卖；角色暂停、伙伴暂停（RF01）各答原因；供应商角色（3.0.39）：采购要它，与客户角色各自开通、暂停；列表按角色筛选（3.0.51）：有生效的角色才列、可限一个法人、暂停的不算、坏参数 400 | `roles`、`partners` |
| `source_integration_test.go` | `source` | 系统面（mTLS）：资格、批量解析（当前、不存在）、来源读取与戳 | `source` |
| `outbox_integration_test.go` | `outbox` | 客户角色的完整状态事件：开通、暂停、恢复各一条 | `roles`、`core` |
| `recoverycheck_integration_test.go` | `recoveryChecks` | 整套恢复的核对：伙伴按版本答“还在不在”（`checksStory`） | 入口 `partner/checks.go` |

共用的：

| 文件 | 内容 |
| --- | --- |
| `endtoend_integration_test.go` | `TestPartnerEndToEnd`（各步的顺序）；`mdm/org` 的替身 `org`；`fixture`（运行中的成员、三个法人、启停替身、步与步之间传的伙伴与角色）；`start`（建世界与替身、迁移并运行成员）；`client.do`、`portFree` |

## 加测试

新流程加一个文件与一步，在 `TestPartnerEndToEnd` 里按依赖排进顺序，在上表加一行；测试不为了通过而改。
