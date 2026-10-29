import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

/** 读取设计 token 的计算值，canvas 和图表取色使用；统一走本函数，不要在各文件复制。 */
export function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

/**
 * credit 大数的可读缩写（万/亿；credit 模式下配额与成本常为亿级整数）。
 * ≥1亿 显示「x.x亿」，≥1万 显示「x.x万」，其余值、负数与 0 原样。
 */
export function fmtCredits(n: number): string {
  const abs = Math.abs(n);
  if (abs >= 1e8) return `${(n / 1e8).toFixed(1)}亿`;
  if (abs >= 1e4) return `${(n / 1e4).toFixed(1)}万`;
  return String(n);
}

/**
 * RFC3339 UTC 时刻转 datetime-local 输入值（本地时区，分钟精度），空串原样返回。
 * 用于把后端过期时刻回填进 datetime-local 选择器。
 */
export function rfc3339ToLocal(v: string): string {
  if (!v) return "";
  const d = new Date(v);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/**
 * credit 整数转精确十进制串（余额展示用，非缩写）。
 * 全程整型运算（安全整数域内精确），不经浮点除法避免二进制尾差；负值带符号。
 */
export function formatCreditsExact(c: number): string {
  const abs = Math.abs(c);
  const int = Math.floor(abs / 1e9);
  const frac = abs % 1e9;
  let s = String(int);
  if (frac > 0) {
    s += "." + String(frac).padStart(9, "0").replace(/0+$/, "");
  }
  if (s === "") s = "0";
  return c < 0 ? `-${s}` : s;
}
