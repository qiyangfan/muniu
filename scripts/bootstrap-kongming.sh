#!/usr/bin/env bash
# 创建 kongming 租户并签发专用 API key。
# 用法:MUNIU_ADMIN_TOKEN=… MUNIU_GATEWAY=http://127.0.0.1:8787 bash scripts/bootstrap-kongming.sh
set -euo pipefail

GATEWAY="${MUNIU_GATEWAY:-http://127.0.0.1:8787}"
TOKEN="${MUNIU_ADMIN_TOKEN:?需要 MUNIU_ADMIN_TOKEN}"
AUTH="Authorization: Bearer ${TOKEN}"

# 创建租户 kongming；已存在时继续执行。
create_out=$(curl -s -X POST "${GATEWAY}/admin/v1/tenants" -H "$AUTH" \
  -H 'Content-Type: application/json' -d '{"name":"kongming"}' || true)
echo "租户: ${create_out}"

# 签发专用 key；当前只设置 RPM 限额。
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
