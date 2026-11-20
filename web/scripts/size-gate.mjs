// 构建产物体积门禁:按 web/.size-limit.json 校验 gzip 后的大小。
// 不用 size-limit CLI:它对 glob 的匹配结果有偶发的「静默跳过」行为,会把未测量
// 误报成通过。这里自己展开 glob、统计并比对本文件声明的 limit,任何一步缺失都失败。
import { readFile, stat } from "node:fs/promises";
import { glob } from "node:fs/promises";
import { createGzip } from "node:zlib";
import { pipeline } from "node:stream/promises";
import { createReadStream } from "node:fs";
import { fileURLToPath } from "node:url";

// parseLimit 把「236 KiB / 8 KiB / 1024 B」这类 limit 解析成字节数。
function parseLimit(limit) {
  const m = /^(\d+(?:\.\d+)?)\s*(B|KiB|kB|MiB)?$/i.exec(limit.trim());
  if (!m) throw new Error(`limit 无法解析: ${limit}`);
  const units = { b: 1, kib: 1024, kb: 1000, mib: 1024 * 1024 };
  return Math.round(Number(m[1]) * (units[(m[2] ?? "B").toLowerCase()] ?? 1));
}

// gzipSize 按构建工具同一口径(level 9)计算 gzip 后字节数。
async function gzipSize(file) {
  let size = 0;
  await pipeline(createReadStream(file), createGzip({ level: 9 }), async function* (source) {
    for await (const chunk of source) size += chunk.length;
  });
  return size;
}

const root = fileURLToPath(new URL("..", import.meta.url));
const config = JSON.parse(await readFile(`${root}.size-limit.json`, "utf8"));
let bad = false;
for (const check of config["size-limit"]) {
  const files = [];
  for await (const f of glob(check.path, { cwd: root })) files.push(`${root}${f}`);
  const limit = parseLimit(check.limit);
  if (files.length === 0) {
    console.log(`size gate FAIL: ${check.name} 未匹配到产物(${check.path}),先跑构建`);
    bad = true;
    continue;
  }
  const size = (await Promise.all(files.map(gzipSize))).reduce((a, b) => a + b, 0);
  const pass = size <= limit;
  if (!pass) bad = true;
  console.log(
    `size gate ${pass ? "PASS" : "FAIL"}: ${check.name} ${size} B / ${limit} B(${files.length} 个文件)`,
  );
}
if (!bad) {
  // 产物文件数兜底:多出来的 chunk 说明入口被拆分,门禁覆盖范围需同步更新
  const all = [];
  for await (const f of glob("dist/assets/*", { cwd: root })) all.push(f);
  if (all.length > config["size-limit"].length) {
    console.log(
      `size gate FAIL: dist/assets 下有 ${all.length} 个产物,配置只覆盖 ${config["size-limit"].length} 个`,
    );
    bad = true;
  }
}
process.exit(bad ? 1 : 0);
