#!/usr/bin/env python3
"""文档相对链接检查：被 git 跟踪的 Markdown 内本地链接必须指向存在的文件与标题锚点。

只检本地链接（http/https/mailto 与纯 # 锚点交给外部链接检查），零网络依赖。
代码围栏与行内代码里的示例链接不参与检查。
用法：scripts/linkcheck.py
"""
import os
import re
import subprocess
import sys

LINK_RE = re.compile(r"\[[^\]]*\]\(([^)\s]+)\)")
FENCE_RE = re.compile(r"^```.*?^```", re.S | re.M)
INLINE_CODE_RE = re.compile(r"`[^`]*`")
HEADING_RE = re.compile(r"^(#{1,6})\s+(.*\S)\s*$", re.M)


def slug(text: str) -> str:
    """GitHub 风格标题锚点：小写、去标点、空格转连字符，保留 CJK 字符。"""
    out = []
    for ch in text.strip().lower():
        if ch in "-_":
            out.append(ch)
        elif ch.isalnum() or ch == " ":
            out.append("-" if ch == " " else ch)
    return "".join(out)


def anchors(text: str) -> set[str]:
    """标题锚点集合；重名标题按 GitHub 规则追加 -1、-2 序号。"""
    seen: dict[str, int] = {}
    out: set[str] = set()
    for m in HEADING_RE.finditer(FENCE_RE.sub("", text)):
        base = slug(m.group(2).strip("# "))
        n = seen.get(base, 0)
        seen[base] = n + 1
        out.add(base if n == 0 else f"{base}-{n}")
    return out


def main() -> int:
    files = subprocess.check_output(["git", "ls-files", "*.md"], text=True).split()
    text_cache: dict[str, str] = {}
    broken: list[str] = []

    def body(path: str) -> str:
        if path not in text_cache:
            with open(path, encoding="utf-8", errors="ignore") as f:
                text_cache[path] = f.read()
        return text_cache[path]

    for f in files:
        text = INLINE_CODE_RE.sub("", FENCE_RE.sub("", body(f)))
        for m in LINK_RE.finditer(text):
            target = m.group(1)
            if target.startswith(("http://", "https://", "mailto:")):
                continue
            path_part, _, anchor = target.partition("#")
            if not path_part:
                if anchor and path_part == "" and f.endswith(".md"):
                    if anchor not in anchors(body(f)):
                        broken.append(f"{f}: 锚点不存在 #{anchor}")
                continue
            resolved = os.path.normpath(os.path.join(os.path.dirname(f), path_part))
            if not os.path.exists(resolved):
                broken.append(f"{f}: 文件不存在 {target} (解析为 {resolved})")
                continue
            if anchor and resolved.endswith(".md") and anchor not in anchors(body(resolved)):
                broken.append(f"{f}: 锚点不存在 {target}")

    if broken:
        for b in broken:
            print(f"linkcheck FAIL: {b}")
        return 1
    print(f"linkcheck PASS: {len(files)} 篇 Markdown 本地链接均可达")
    return 0


if __name__ == "__main__":
    sys.exit(main())
