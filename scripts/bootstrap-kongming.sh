#!/usr/bin/env bash
# T5.1 kongming 灰度接入准备:创建 kongming 租户并签发专用 key(经管理 API)。
# 用法:MUNIU_ADMIN_TOKEN=… MUNIU_GATEWAY=http://127.0.0.1:8787 bash scripts/bootstrap-kongming.sh
# 限额依据(traffic-governance 流程):kongming 现有用量 ×2 起步,观察一周后回调。
set -euo pipefail

GATEWAY="${MUNIU_GATEWAY:-http://127.0.0.1:8787}"
TOKEN="${MUNIU_ADMIN_TOKEN:?需要 MUNIU_ADMIN_TOKEN}"
AUTH="Authorization: Bearer ${TOKEN}"

# 1) 创建租户 kongming(已存在则忽略报错)
create_out=$(curl -s -X POST "${GATEWAY}/admin/v1/tenants" -H "$AUTH" \
  -H 'Content-Type: application/json' -d '{"name":"kongming"}' || true)
echo "租户: ${create_out}"

# 2) 打开 kongming 租户语义缓存开关(灰度期建议先关,确认基线后再开;此处默认关)
# 3) 签发专用 key(rpm 120 / tpm 0 不限 / 日配额 0 不限,正式承载前按用量 ×2 回填)
key_out=$(curl -s -X POST "${GATEWAY}/admin/v1/keys" -H "$AUTH" \
  -H 'Content-Type: application/json' \
  -d '{"tenant":"kongming","name":"kongming-primary","rpm_limit":120}')
echo "${key_out}"
key=$(echo "${key_out}" | sed -n 's/.*"key":"\([^"]*\)".*/\1/p')
if [ -z "${key}" ]; then echo "签发失败"; exit 1; fi

echo
echo "!! 明文仅此一次,请写入 kongming 的 .env(不要入库):"
echo "   MUNIU_KEY=${key}"
echo "   kongming 侧 base_url 改为: ${GATEWAY}/v1"
