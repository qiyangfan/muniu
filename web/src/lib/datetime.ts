/** 时间与数字格式化单源(与 kongming/jinguan 两仓 lib/datetime.ts 同构)。
 *  用户可见时间一律走本文件:禁页面内直接渲染 ISO 串、禁裸 toLocaleString()。 */

const pad = (n: number): string => String(n).padStart(2, "0");

function toDate(v: string | Date | null | undefined): Date | null {
  if (v == null || v === "") return null;
  const d = v instanceof Date ? v : new Date(v);
  return Number.isNaN(d.getTime()) ? null : d;
}

/** 本地时区 "2026-09-12 16:30"(分钟精度);seconds 补秒;空/非法值返回 "—" */
export function formatDateTime(
  v: string | Date | null | undefined,
  opts?: { seconds?: boolean },
): string {
  const d = toDate(v);
  if (!d) return "—";
  const base = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return opts?.seconds ? `${base}:${pad(d.getSeconds())}` : base;
}

/** 本地时区 "2026-09-12";空/非法值返回 "—" */
export function formatDate(v: string | Date | null | undefined): string {
  const d = toDate(v);
  return d ? `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` : "—";
}

/** 耗时:<1s 显 "840ms";<60s 显 "8.3s"(整秒去小数);更长显 "3m25s";空值 "—" */
export function formatDuration(ms: number | null | undefined): string {
  if (ms == null || Number.isNaN(ms)) return "—";
  if (ms < 1000) return `${Math.min(Math.round(ms), 999)}ms`;
  const s = ms / 1000;
  if (s < 60) {
    // 舍入进位钳到 59s:59.95s 显 "60s" 会与下一档 "1m0s" 口径相撞
    const v = Math.min(s < 10 ? Number(s.toFixed(1)) : Math.round(s), 59);
    return `${v}s`;
  }
  const m = Math.floor(s / 60);
  const sec = Math.round(s % 60);
  return sec === 60 ? `${m + 1}m0s` : `${m}m${sec}s`;
}

/** 整数千分位:1234567 → "1,234,567";空值 "—" */
export function formatNumber(n: number | string | null | undefined): string {
  if (n == null || n === "") return "—";
  const num = typeof n === "string" ? Number(n) : n;
  return Number.isNaN(num) ? String(n) : num.toLocaleString("zh-CN");
}
