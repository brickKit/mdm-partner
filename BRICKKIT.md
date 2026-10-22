# mdm/partner

## 组件定位

业务伙伴（企业或自然人）。有销售或采购业务的客户都需要；只用人事或项目的客户可以不装。
- **伙伴**：身份永不复用；名称与译名、国家、税号、正式联系人与地址。同一国家的税号重复时给出提示，由人决定是否合并（D22），不拒绝。生命周期按协议 07：启用、暂停、退役。
- **角色**：按法人开通。新开通时同步问 `mdm/org` 法人是否存在、是否启用；`mdm/org` 答不上时不开通（协议 07：不可达不等于不存在）。角色有自己的启用、暂停、退役，与伙伴的状态分开。
- **资格查询**：新单据能否在某法人以某用途引用某伙伴。伙伴与该用途的角色都启用才可以；回答说明缺的是哪一部分，并带取证时刻（协议 07 的有效期 W 为 30 秒，从调用方发出查询时算起，调用方在 W 之内提交，或重新取证）。资格只看伙伴与角色，不看法人的状态：法人的证据由调用方另向 `mdm/org` 取。
- **批量解析**：按身份回答当前数据、已并入（附存续伙伴）、墓碑或不存在。

信用额度不在这里（`erp/credit`），CRM 私有的关系不在这里（`crm/customer`）。

实验第 6 步的范围：只有客户角色；扩展字段（协议 08）还没有，等 SDK 支持。供应商、员工往来与关联方角色、合并与拆回、伙伴银行账户、销毁与擦除都在契约里，调用答“能力不支持”（`CAPABILITY_UNSUPPORTED`，D75）。

## 部署前准备

- 数据库：PostgreSQL 16 及以上；schema `mdm_partner`，属主角色 `mdm_partner_owner`（只供迁移）与运行角色 `mdm_partner_rw`（只有读写数据的权限），名字来自装配项目的注册表。三者由部署方在第一次启动前建好（be-assembly-standard 的 `make db-init` 按装配进项目的组件配置生成并执行）；表由本组件的迁移建，迁移里不建角色、不授权。
- 消息系统（`BUS_URL`）与实现实例（`IMPLEMENTATION_INSTANCE`）：每次变化以完整状态事件发布到流 `mdm.partner`（4 个分区）。
- 部署的服务证书（`SERVICE_TLS_*`）；身份与授权（`IAM_*`、`AUTHZ_GRPC_URL`、`DEPLOYMENT_ID`）；审计（`AUDIT_GRPC_URL`）。
- 依赖 `mdm/org`：经它的系统面确认法人。

## 依赖说明

- 必需：数据库、消息系统、身份成员、授权成员、`mdm/org`。
- 可选：审计组件。
- `mdm/org` 不可达时：已有的数据照常读写；新开通角色被拒，直到它回来。

## 配置指南

全部键见 `component.yaml` 的 `configSchema`（由 SDK 生成，测试核对）。

| 键 | 说明 |
| --- | --- |
| `DEPLOYMENT_ID`、`IAM_ISSUER`、`IAM_JWKS_URL`、`AUTHZ_GRPC_URL`、`SERVICE_TLS_CERT_FILE`、`SERVICE_TLS_KEY_FILE`、`SERVICE_TLS_CA_FILE`（必填） | 受保护的用户面与 mTLS 的系统面要用；部署身份、身份与授权的地址在外壳里是进程级的键 |
| `PG_HOST`、`PG_DATABASE`、`PG_USER`、`PG_PASSWORD`、`PG_SCHEMA`（必填） | 自己的数据库，密码是密钥；迁移另用 `PG_OWNER_USER`、`PG_OWNER_PASSWORD` |
| `BUS_URL`（必填，密钥）、`IMPLEMENTATION_INSTANCE`（必填） | 消息系统；实现实例是安装时生成的随机身份，事件与来源方法的回答都带它（协议 02） |
| `AUDIT_GRPC_URL` | 审计组件的系统面；缺时审计记录留在本地，按协议 03 处理 |

mdm/org 的地址由 BrickKit 按依赖注入（`MDM_ORG_GRPC_ENDPOINT`），不是配置键。

## 契约索引

- **系统面**（本仓库 `contract/proto`，模块 `github.com/brickKit/mdm-partner/contract/v3`，标签 `contract/v3.…`）：
  - `be.mdm.partner.v3.SourceService/Snapshot`、`Read`：协议 02 的来源方法，聚合是伙伴与角色；回答带部署、实现实例、纪元与各分区位置高点；一次最多 500 个。
  - `be.mdm.partner.v3.PartnerService/CheckEligibility`：资格查询（用途 `PURPOSE_SELL` 需要客户角色）。
  - `be.mdm.partner.v3.PartnerService/Resolve`：批量解析，一次最多 500 个。
- **用户面**：`contract/openapi.yaml`。操作 `mdm.partner.read`、`mdm.partner.create`、`mdm.partner.edit`、`mdm.partner.merge`、`mdm.partner_role.manage`。新建伙伴要带 `Idempotency-Key`。
- **事件**（流 `mdm.partner`，schema 在 `contract/events/`）：`mdm.partner.changed.v1`、`mdm.partner_role.changed.v1`，都是完整状态。

## 外壳声明

不是外壳。可以被 Go 的基础外壳（`be/go-infra`）承载。
