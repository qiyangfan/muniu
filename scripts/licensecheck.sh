#!/usr/bin/env bash
# 依赖许可证白名单审计:go.mod 直接/间接依赖的许可证必须落在允许清单内。
# 新增依赖带出清单外许可证时,先记 HANDOFF 开放项评估,再决定扩清单或换依赖。
set -euo pipefail

allowed="MIT,Apache-2.0,BSD-2-Clause,BSD-3-Clause,CC0-1.0"
go-licenses check ./... --allowed_licenses="${allowed}"
