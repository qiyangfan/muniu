/** 时间与数字格式化单源(与 kongming/jinguan 两仓 lib/datetime.ts 同族,本仓只留在用函数;
 *  未用到的日期/耗时格式化随 knip 门禁删除,需要时从两仓同构函数回补)。
 *  用户可见时间一律走本文件:禁页面内直接渲染 ISO 串、禁裸 toLocaleString()。 */

const pad = (n: number): string => String(n).padStart(2, "0");

function toDate(v: string | Date | null | undefined): Date | null {
  if (v == null || v === "") return null;
  const d = v instanceof Date ? v : new Date(v);
  return Number.isNaN(d.getTime()) ? null : d;
}

/** 按本地时区格式化为 YYYY-MM-DD HH:mm；可选显示秒，空值或非法值返回 "—"。 */
export function formatDateTime(
  v: string | Date | null | undefined,
  opts?: { seconds?: boolean },
): string {
  const d = toDate(v);
  if (!d) return "—";
  const base = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return opts?.seconds ? `${base}:${pad(d.getSeconds())}` : base;
}

/** 整数千分位:1234567 → "1,234,567";空值 "—" */
export function formatNumber(n: number | string | null | undefined): string {
  if (n == null || n === "") return "—";
  const num = typeof n === "string" ? Number(n) : n;
  return Number.isNaN(num) ? String(n) : num.toLocaleString("zh-CN");
}
