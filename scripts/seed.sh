#!/usr/bin/env bash
# 本组件的种子数据：15 个示例客户，覆盖 ACTIVE / DISABLED 两种状态、多个行业、
# 信用额度从 0 到百万级，以及几条给关键字搜索 q 演示用的样例（code 以 HN 开头
# 的两条是前缀匹配，名称里含 "CHN" 的一条只是包含匹配：q=hn 时前两条排在前面）。
#
# seed-customer-1..5 是下游组件（crm/opportunity 等）的种子脚本按幂等键反查
# 引用的固定客户：只增不改，新客户一律往后接。
#
# 只给本地开发 / 演示用，不出现在任何部署或 CI 流程里。全程走真实 gRPC 调用，
# 幂等键固定，重复跑不会重复建。唯一的例外是末尾的"时间跨度回填"：直接
# UPDATE created_at，让列表的默认时间窗口与排序有新旧之分可看（customers 不分区，
# 改 created_at 没有分区放错的风险）。
#
# 没有依赖：只需要本组件自己在跑（make -C components/mdm/customer seed）。
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
source "$ROOT/infra/scripts/lib/seed-net.sh"

need docker; need python3

seed_net_check

GRPC_PORT="$(awk -F'\t' '$2=="mdm/customer"{print $4}' "$ROOT/registry/ports.tsv")"
[ -n "$GRPC_PORT" ] || die "registry/ports.tsv 里找不到 mdm/customer 的 grpc 端口"

# gRPC 额外端口不映射到宿主机：grpcurl 用容器镜像加入项目网络直连服务名。
GRPCURL="docker run --rm --network $NET -v $DIR/contracts:/contracts:ro fullstorydev/grpcurl:latest"
TARGET="$(service_name mdm/customer):$GRPC_PORT"

# mkcustomer <幂等键> <名称> <信用额度> [code]：code 不给就由组件自动编号。
mkcustomer() {
  local key="$1" name="$2" credit="$3" code="${4:-}"
  $GRPCURL -plaintext -import-path /contracts -proto mdm/customer/v1/customer.proto \
    -d "{\"idempotency_key\":\"$key\",\"code\":\"$code\",\"name\":\"$name\",\"credit_limit\":\"$credit\"}" \
    "$TARGET" mdm.customer.v1.CustomerService/Create \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["customer"]["id"])'
}

setstatus() {
  local key="$1" id="$2" status="$3"
  # SetStatus 带乐观锁：先 Get 拿当前 version。
  local version
  version="$($GRPCURL -plaintext -import-path /contracts -proto mdm/customer/v1/customer.proto \
    -d "{\"id\":\"$id\"}" "$TARGET" mdm.customer.v1.CustomerService/Get \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["version"])')"
  $GRPCURL -plaintext -import-path /contracts -proto mdm/customer/v1/customer.proto \
    -d "{\"idempotency_key\":\"$key\",\"id\":\"$id\",\"version\":$version,\"status\":\"$status\"}" \
    "$TARGET" mdm.customer.v1.CustomerService/SetStatus >/dev/null
}

addcontact() {
  local key="$1" id="$2" name="$3" phone="$4" email="$5"
  $GRPCURL -plaintext -import-path /contracts -proto mdm/customer/v1/customer.proto \
    -d "{\"idempotency_key\":\"$key\",\"customer_id\":\"$id\",\"contact\":{\"name\":\"$name\",\"phone\":\"$phone\",\"email\":\"$email\",\"primary\":true}}" \
    "$TARGET" mdm.customer.v1.CustomerService/AddContact >/dev/null
}

echo "── mdm-customer：灌 15 个示例客户（1-5 被下游种子脚本引用，只增不改）──"
C1="$(mkcustomer seed-customer-1 "「本地测试」华南电子科技有限公司" 500000.00)"
addcontact seed-customer-1-contact "$C1" "张经理" "13800000001" "zhang@example.com"
C2="$(mkcustomer seed-customer-2 "「本地测试」京城机械制造集团" 300000.00)"
C3="$(mkcustomer seed-customer-3 "「本地测试」江南纺织实业公司" 200000.00)"
C4="$(mkcustomer seed-customer-4 "「本地测试」西部矿业贸易公司" 800000.00)"
C5="$(mkcustomer seed-customer-5 "「本地测试」滨海物流仓储公司（已停用样例）" 150000.00)"
setstatus seed-customer-5-disable "$C5" CUSTOMER_STATUS_DISABLED

# 6-12：更多行业，信用额度从 0 到百万级。
C6="$(mkcustomer seed-customer-6 "「本地测试」都市零售连锁超市" 50000.00)"
C7="$(mkcustomer seed-customer-7 "「本地测试」云帆软件科技有限公司" 1000000.00)"
addcontact seed-customer-7-contact-1 "$C7" "李总" "13800000007" "li@example.com"
addcontact seed-customer-7-contact-2 "$C7" "王助理" "13800000107" "wang.assist@example.com"
# 信用额度刻意压得极低：用这个客户建一张金额稍大的订单，就能演示 erp/sales
# 确认订单时信用超限被拒绝、补偿回滚的那条路径。
C8="$(mkcustomer seed-customer-8 "「本地测试」鲜达食品饮料公司（低信用额度样例）" 5000.00)"
C9="$(mkcustomer seed-customer-9 "「本地测试」恒基建筑工程公司" 600000.00)"
C10="$(mkcustomer seed-customer-10 "「本地测试」新能源电力集团" 900000.00)"
# 零信用额度 + 停用，两种边界叠一起的组合样例。
C11="$(mkcustomer seed-customer-11 "「本地测试」丰收农业发展公司（零额度+已停用样例）" 0.00)"
setstatus seed-customer-11-disable "$C11" CUSTOMER_STATUS_DISABLED
C12="$(mkcustomer seed-customer-12 "「本地测试」康泰医疗器械公司" 400000.00)"

# 13-15：关键字搜索 q 的样例。q=hn 时 13、14 是 code 前缀匹配（大小写不敏感），
# 15 只是名称里包含 "HN"，排在后面；q=华南 时 1、13、14、15 都命中（名称都以
# 「本地测试」开头，所以都是包含匹配）。
C13="$(mkcustomer seed-customer-13 "「本地测试」华南精密模具公司" 250000.00 HN-001)"
C14="$(mkcustomer seed-customer-14 "「本地测试」华南智能仓储公司" 120000.00 HN-002)"
C15="$(mkcustomer seed-customer-15 "「本地测试」CHN 华南国际贸易公司" 350000.00)"

ok "客户：$C1(ACTIVE+1联系人) $C2(ACTIVE) $C3(ACTIVE) $C4(ACTIVE) $C5(DISABLED) $C6(ACTIVE) $C7(ACTIVE+2联系人) $C8(ACTIVE,低额度) $C9(ACTIVE) $C10(ACTIVE) $C11(DISABLED,零额度) $C12(ACTIVE) $C13(HN-001) $C14(HN-002) $C15(含CHN)"

# ── 时间跨度回填：6、7、9、10 改成 1-4 个月前建档，列表的默认时间窗口（最近
# 90 天）与排序因此有新旧之分：6（4 个月前）默认窗口里看不到，要给 created_after。
# 1-5 被下游引用，创建时间不动。
psqlx -q <<SQL
SET search_path TO mdm_customer;
UPDATE customers SET created_at = now() - interval '4 months', updated_at = now() - interval '4 months' WHERE id = '$C6';
UPDATE customers SET created_at = now() - interval '3 months', updated_at = now() - interval '3 months' WHERE id = '$C7';
UPDATE customers SET created_at = now() - interval '2 months', updated_at = now() - interval '2 months' WHERE id = '$C9';
UPDATE customers SET created_at = now() - interval '1 months', updated_at = now() - interval '1 months' WHERE id = '$C10';
SQL
ok "已给 4 个客户回填历史创建时间（1-4 个月前），列表不再全部挤在同一秒"
