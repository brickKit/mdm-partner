# mdm-partner 的契约

`mdm/partner` 发布的契约包：系统面的 protobuf、事件载荷的 JSON Schema、用户面的 OpenAPI。被调方发布契约，放在组件仓库的 `contract/` 子目录里单独发布，不另建仓库。

依据：装配项目 [be-assembly-standard](https://github.com/brickKit/be-assembly-standard) 的协议 02（S1–S3）、协议 07（资格查询与解析）、协议 14、边界卡 `mdm/partner`、D20–D22。

## 内容

| 位置 | 是什么 |
| --- | --- |
| `proto/be/mdm/partner/v3/partner.proto` | `SourceService.Snapshot`、`Read`（来源方法）；`PartnerService.CheckEligibility`、`Resolve` |
| `gen/go/be/mdm/partner/v3/` | Go 代码，包 `partnerv3`；模块 `github.com/brickKit/mdm-partner/contract/v3` |
| `python/` | Python 包 `contract-mdm-partner`，模块 `be.mdm.partner.v3` |
| `events/` | `mdm.partner.changed.v1`、`mdm.partner_role.changed.v1` 的载荷，同时是来源方法里 `data` 的格式 |
| `openapi.yaml` | 用户面；每个路由的操作写在 `x-operation` |

## 来源的义务（协议 02 的 S1–S3）

与 `mdm-currency`、`mdm-org` 的契约相同：每次变化版本加一，没有变化不产生新版本；同一事务写完整状态事件，流 `mdm.partner`，4 个分区；按身份读取一个身份一个结果、顺序相同，没有的答“已清理”，一次最多 500 个；快照按身份升序分页；每次回答带部署、实现实例、纪元与各分区位置高点，取自同一个读快照。

## 资格与解析（协议 07）

- **资格**：伙伴启用、该用途的角色在该法人启用，才 `eligible`；否则 `reason` 说明缺的部分：`not_found`、`partner_suspended`、`partner_retired`、`role_not_opened`、`role_suspended`、`role_retired`（合并与销毁之后另有 `partner_merged`、`partner_destroyed`）。回答带取证时刻；调用方从发出查询时起在 W（30 秒）之内提交，过期重新取证，不在事务里调用（协议 07）。
- **解析**：每个身份一个结果：当前（附数据）、已并入（附别名链末端的存续伙伴）、墓碑、不存在。“不可见”只在用户面出现；“不可达”是调用方自己的结论，不能当作不存在。
