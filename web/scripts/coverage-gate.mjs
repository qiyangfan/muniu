// 逐文件覆盖率下限:默认 50%(行),低覆盖文件在此冻结,只防回退。
// 新增文件达不到默认下限时,要么补测试,要么登记并写明原因。
import { readFileSync } from "node:fs";

const summary = JSON.parse(
  readFileSync(new URL("../coverage/coverage-summary.json", import.meta.url), "utf8"),
);

const defaultFloor = 50;
const floors = {
  "src/main.tsx": 0, // 应用入口,只有 createRoot 装配,无独立行为
  "src/pages/Usage.tsx": 7,
  "src/lib/utils.ts": 38,
  "src/lib/api.ts": 40, // 组件测试经 vi.mock 替换,真实实现不在单测路径内
};

const rel = (p) => p.slice(p.indexOf("/web/") + 5);
let bad = 0;
let files = 0;
for (const [file, metrics] of Object.entries(summary)) {
  if (file === "total") continue;
  files++;
  const key = rel(file);
  const floor = key in floors ? floors[key] : defaultFloor;
  const pct = metrics.lines.pct;
  if (pct + 1e-9 < floor) {
    console.log(`coverage gate FAIL: ${key} ${pct.toFixed(1)}% < ${floor}%`);
    bad = 1;
  }
}
if (bad) process.exit(1);
console.log(`coverage gate PASS: 逐文件下限已校验(${files} 个文件)`);
