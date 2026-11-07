# source：系统面

给部署里其他组件的读取（装配项目协议 02 的来源方法）与协议 07 的资格查询、批量解析；任何组件都可调。

## 负责与不负责

- 负责：`SourceService.Read`、`Snapshot`（伙伴与角色两种记录，带戳）；`PartnerService.CheckEligibility`、`Resolve`（当前、不存在）；一次最多 `core.MaxBatch` 个。
- 不负责：资格的规则（`roles`）；记录怎样写（`partners`、`roles`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `source.go` | `aggregates`、`stamp`、`readByID`、`Snapshot`、`Read`、`purposes`、`CheckEligibility`、`Resolve` |

## 依赖与被依赖

- 依赖：`core`、`roles`（`EligibilityOf`）。
- 被依赖：入口 `partner/services.go`。

## 规则在哪

装配项目协议 02（来源要提供什么、戳）、协议 07（资格与解析）、决定 0304（批量的上限）。

## 测试

`itest/source_integration_test.go`（`source`）。
