#!/usr/bin/env bash
# 提交信息规范检查:标题必须为 <type>(<scope>): <描述>。
# --file <path> 供 pre-commit 的 commit-msg 阶段使用;传 <base>..<head> 供 CI 枚举范围内提交。
# 合并/回退/fixup 提交放行:它们由 git 或变基工具生成,标题不可控。
set -euo pipefail

pattern='^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([^)]+\))?!?: .+$'
allow='^(Merge |Revert |fixup! |squash! |amend! )'

bad=0
check_subject() {
  local subject="$1" where="$2"
  if [[ "$subject" =~ $allow ]]; then
    return 0
  fi
  if [[ ! "$subject" =~ $pattern ]]; then
    printf '提交信息不合规(%s): %s\n' "$where" "$subject" >&2
    bad=1
  fi
}

if [ "${1:-}" = "--file" ]; then
  file="${2:?--file 需要提交信息文件路径}"
  # 标题是首个非空、非注释行
  subject=$(grep -m1 -v -E '^[[:space:]]*(#|$)' "$file" || true)
  check_subject "$subject" "$file"
else
  range="${1:?用法: commitcheck.sh --file <path> | <base>..<head>}"
  while IFS= read -r subject; do
    check_subject "$subject" "$range"
  done < <(git log --format=%s "$range")
fi

if [ "$bad" -ne 0 ]; then
  echo "规范: <type>(<scope>): <描述>;type ∈ feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert" >&2
  exit 1
fi
