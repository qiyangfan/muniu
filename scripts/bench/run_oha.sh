#!/usr/bin/env bash
# T5.4 SSE 压测执行器(oha):并发 100/500/1000/5000 四档。
# 前置:make build && ./bin/muniu -c <压测配置> &;python3 scripts/bench/fake_upstream.py 59990 20 0.01 &
# 产物:原始数字贴入 docs/benchmarks/<日期>.md
set -euo pipefail
command -v oha >/dev/null || { echo "需要 oha(brew install oha)"; exit 1; }

GATEWAY="${MUNIU_GATEWAY:-http://127.0.0.1:8787}"
KEY="${MUNIU_KEY:?需要 MUNIU_KEY(压测专用 key)}"
OUT="${OUT:-docs/benchmarks/raw-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "${OUT}"

MODEL="${MODEL:-bench-alias}"
BODY="{\"model\":\"${MODEL}\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"

# 失败档位必须留痕并让脚本非零退出:吞掉失败会把空/截断 JSON 当有效压测结果归档
failed=0
for c in 100 500 1000 5000; do
  echo "=== 并发 ${c} ==="
  if oha -n $((c * 20)) -c "$c" -m POST \
    -H "Authorization: Bearer ${KEY}" -H "Content-Type: application/json" \
    -d "${BODY}" \
    -t 120s --no-tui \
    --output-format json \
    "${GATEWAY}/v1/chat/completions" > "${OUT}/sse-c${c}.json" && [ -s "${OUT}/sse-c${c}.json" ]; then
    continue
  fi
  echo "!!! 并发 ${c} 档失败或产物为空: ${OUT}/sse-c${c}.json" >&2
  failed=1
done
if [ "${failed}" -ne 0 ]; then
  echo "存在失败档位,数据不可用于报告" >&2
  exit 1
fi
echo "原始数据已写入 ${OUT}/"
