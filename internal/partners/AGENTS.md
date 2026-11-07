# partners：伙伴

业务伙伴（组织与个人）本身：名称、国家、税号、正式的联系人与地址、状态。

## 负责与不负责

- 负责：表 `partner`；路由 `/api/partner/partners` 的列表、新建（幂等键：伙伴没有天然的唯一键）、读取、修改、状态；事件 `mdm.partner.changed.v1`；税号的规范化与“同国家同税号”的提示（不拒绝，D22）；搜索列（拼音首字母）。
- 不负责：角色与资格（`roles`）；给其他组件的读取（`source`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `partners.go` | `partnerIn` 与 `apply`（输入的检查与规范化）、`searchOf`；`CreatePartner`（幂等、查重的提示）、`answer`、`answerWith`；`Partner`（读一个，带授权）、`GetPartner`；`changePartner`、`UpdatePartner`（版本守卫）、`PartnerState`；`CheckCurrency`；列表 `partnerList`、`ListPartners` |

## 依赖与被依赖

- 依赖：`core`。
- 被依赖：`roles`（`Partner`、`CheckCurrency`）；入口登记路由。

## 规则在哪

`docs/design.md` 的状态与规则；装配项目 D20–D22、协议 14（列表）。

## 测试

`itest/partners_integration_test.go`（`partners`、`partnerEdits`）。
