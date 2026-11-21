#!/usr/bin/env bash
# 依赖许可证白名单审计:go.mod 直接/间接依赖的许可证必须落在允许清单内。
# 新增依赖带出清单外许可证时,先记 HANDOFF 开放项评估,再决定扩清单或换依赖。
set -euo pipefail

allowed="MIT,Apache-2.0,BSD-2-Clause,BSD-3-Clause,CC0-1.0"
# modernc.org/mathutil 的 LICENSE 是标准 BSD-3-Clause 文本,但版权行写作
# "The mathutil Authors",go-licenses 的许可分类器无法识别。这里不跳过审计:
# 先排除该模块,再对其 LICENSE 做精确哈希与条款断言;版本/文本一变即失败。
unclassifiable="modernc.org/mathutil"
go-licenses check ./... --allowed_licenses="${allowed}" --ignore "${unclassifiable}"

dir="$(go list -m -f '{{.Dir}}' "${unclassifiable}")"
license="${dir}/LICENSE"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "${license}" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "${license}" | awk '{print $1}')"
fi
expected="bfa9bf72a72ca009fd62a8f84fca3dca67e51d93af96352723646599898b6cf5"
if [ "${actual}" != "${expected}" ]; then
  echo "modernc.org/mathutil LICENSE 文本已变化,须重新评审:${actual}" >&2
  exit 1
fi
grep -q "Redistribution and use in source and binary forms" "${license}"
grep -q "Neither the names of the authors" "${license}"
