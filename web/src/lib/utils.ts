import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

/** 读取设计 token 计算值(canvas/图表取色用)。全站单源,禁再各文件复制本函数(宪法 Article 3)。 */
export function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

/**
 * credit 大数的人类可读缩写(万/亿;credit 模式下配额与成本常为亿级整数)。
 * ≥1亿 → "x.x亿";≥1万 → "x.x万";其余原值。负数与 0 原样。
 */
export function fmtCredits(n: number): string {
  const abs = Math.abs(n);
  if (abs >= 1e8) return `${(n / 1e8).toFixed(1)}亿`;
  if (abs >= 1e4) return `${(n / 1e4).toFixed(1)}万`;
  return String(n);
}

/**
 * RFC3339(UTC)→ datetime-local 输入值(本地时区,分钟精度);空串原样返回。
 * 用于把后端过期时刻回填进 datetime-local 选择器。
 */
export function rfc3339ToLocal(v: string): string {
  if (!v) return "";
  const d = new Date(v);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
