#!/usr/bin/env bash
# deadcode 即使报出不可达函数也退出 0,门禁以「输出非空」判定失败。
set -euo pipefail

out=$(deadcode -test ./...)
if [ -n "${out}" ]; then
	echo "${out}"
	exit 1
fi
