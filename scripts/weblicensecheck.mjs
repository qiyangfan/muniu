#!/usr/bin/env node
// 控制台生产依赖许可证白名单审计：只审随产物分发的依赖（--prod），开发链不参与。
// 白名单与 Go 侧 scripts/licensecheck.sh、Rust 侧 plugins/deny.toml 保持同一口径。
// 复合表达式（如 "MIT AND ISC"）按 AND/OR 拆分后逐项判定。
import { execFileSync } from "node:child_process";
import process from "node:process";

const allowed = new Set([
  "MIT",
  "ISC",
  "Apache-2.0",
  "BSD-2-Clause",
  "BSD-3-Clause",
  "0BSD",
  "CC0-1.0",
  "Unlicense",
]);

let raw;
try {
  raw = execFileSync("pnpm", ["licenses", "list", "--prod", "--json"], {
    encoding: "utf8",
    maxBuffer: 32 * 1024 * 1024,
  });
} catch (e) {
  console.error(`[web-licenses] 读取依赖许可证失败: ${e.message}`);
  process.exit(1);
}

// pnpm 输出按许可证分组的 { license: [pkg...] }；devDependencies 在 --prod 下不出现。
const grouped = JSON.parse(raw);
const violations = [];
let count = 0;

for (const [expr, pkgs] of Object.entries(grouped)) {
  const parts = expr.split(/\s+(?:AND|OR)\s+/i).map((s) => s.trim());
  const ok = parts.every((p) => allowed.has(p));
  if (ok) {
    count += pkgs.length;
    continue;
  }
  for (const p of pkgs) {
    violations.push(`${p.name}@${p.versions?.join(",")} → ${expr}`);
  }
  count += pkgs.length;
}

if (violations.length > 0) {
  for (const v of violations) console.error(`web-licenses FAIL: ${v}`);
  process.exit(1);
}
console.log(`web-licenses PASS: ${count} 个生产依赖许可证均在白名单内`);
