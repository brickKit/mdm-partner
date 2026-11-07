# core：全组件共用的名字与写法（共用）

每个模块都引用、自己什么业务也不做的东西。改这里会影响所有模块。

## 负责与不负责

- 负责：组件 ID、依赖 `mdm/org`、流与事件类型、资源与操作、`MaxBatch`、`W`、资源定义 `Definitions()`（`names.go`）；本组件的错误原因、身份的格式、授权检查、校验、审计（`Change`）、内容比较（`Same`）、本版不做的路由（`Unsupported`）（`rules.go`）；记录的类型、读取、税号查重（`Duplicates`）、发件表（`Emit`）（`records.go`）；`Base`（`base.go`）。
- 不负责：任何业务规则（在 `partners`、`roles`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `base.go` | `Base`：运行时、部署、实现实例 |
| `names.go` | 常量；`Definitions()` |
| `rules.go` | 错误原因、`UUIDForm` 等格式、`Unsupported`、`Check`、`Invalid`、`NotFound`、`Change`、`Same` |
| `records.go` | `Partner`、`Contact`、`Address`、`Role` 与 `Scan…`、`Get…`；`Duplicates`（同一国家里规范化后相同的税号）；`Emit` |

## 依赖与被依赖

- 依赖：只有 SDK 与契约。
- 被依赖：全部模块与入口。

## 规则在哪

`docs/design.md`；装配项目协议 02、07、14，D20–D22。

## 测试

由 `itest/` 的流程测试间接覆盖。
