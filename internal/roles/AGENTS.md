# roles：客户、供应商角色与资格

伙伴按法人开通的客户与供应商角色（各自开通、各有状态与缺省；员工往来、关联方以后），以及资格：新单据此刻能不能以某种用途引用这个伙伴（装配项目协议 07）。

## 负责与不负责

- 负责：表 `partner_role`；路由 `/api/partner/partners/:id/roles` 的列表、客户与供应商角色的开通与状态（同一套函数 `putRole`、`roleState`，按角色种类）、`/api/partner/eligibility`；事件 `mdm.partner_role.changed.v1`；开通前在事务外问 `mdm/org` 法人能不能用（`legalEntityActive`）。
- 不负责：伙伴本身（`partners`）；系统面的资格查询（`source`，它调这里的 `EligibilityOf`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `roles.go` | `ListRoles`；`legalEntityActive`；`roleIn`、`PutCustomerRole`、`PutSupplierRole`（都经 `putRole`）、`CustomerRoleState`、`SupplierRoleState`（都经 `roleState`）；`Eligibility`、`roleOf`、`EligibilityOf`（伙伴暂停、角色没开、角色暂停各有原因；不看法人的状态）、`EligibilityHTTP` |

## 依赖与被依赖

- 依赖：`core`、`partners`。
- 被依赖：`source`（`EligibilityOf`）；入口登记路由。

## 规则在哪

`docs/design.md` 的状态与规则、未决问题；装配项目协议 07（资格是此刻的证据，调用方在 W 之内提交；不可达不是不存在）。

## 测试

`itest/roles_integration_test.go`（`roles`，含 `mdm/org` 不可达）、`itest/eligibility_integration_test.go`（`eligibility`）；系统面的资格在 `itest/source_integration_test.go`。
