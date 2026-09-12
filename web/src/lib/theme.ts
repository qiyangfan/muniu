/* 主题偏好单源(契约对齐 kongming/jinguan 的 lib/theme-store.ts):
   「localStorage 手动值 → prefers-color-scheme → 亮色」,与 index.html 内联防 FOUC
   脚本为同一契约(key muniu-theme 与优先级钉死,双侧注释互指,改动必须同步)。
   muniu 无 zustand,以纯函数 + hook 实现;手动切换写入 localStorage 并从此优先于系统偏好;
   系统偏好实时跟随仅无手动值时生效(宪法 Article 10)。 */
import { useCallback, useEffect, useState } from "react";

export const THEME_STORAGE_KEY = "muniu-theme";

export type Theme = "light" | "dark";

function readStored(): Theme | null {
  try {
    const v = localStorage.getItem(THEME_STORAGE_KEY);
    return v === "light" || v === "dark" ? v : null;
  } catch {
    return null; // 隐私模式极端配置:读不到按无手动值处理,功能降级不报错
  }
}

function systemTheme(): Theme {
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

/** 契约解析(与 index.html 内联脚本同语义) */
export function resolveTheme(): Theme {
  return readStored() ?? systemTheme();
}

function applyToDom(theme: Theme): void {
  document.documentElement.classList.toggle("dark", theme === "dark");
}

/** 渲染态主题 + 手动切换(翻转生效主题,写入 localStorage 并优先于系统偏好)。 */
export function useTheme(): { theme: Theme; toggle: () => void } {
  const [theme, setTheme] = useState<Theme>(resolveTheme);

  // 系统偏好实时跟随:有手动值时忽略系统变化(手动值优先,不逆转用户选择)
  useEffect(() => {
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = (e: MediaQueryListEvent) => {
      if (readStored() !== null) return;
      const next: Theme = e.matches ? "dark" : "light";
      applyToDom(next);
      setTheme(next);
    };
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);

  const toggle = useCallback(() => {
    const next: Theme = resolveTheme() === "dark" ? "light" : "dark";
    try {
      localStorage.setItem(THEME_STORAGE_KEY, next);
    } catch {
      /* 写失败(隐私模式):本次会话内仍生效,持久化降级 */
    }
    applyToDom(next);
    setTheme(next);
  }, []);

  return { theme, toggle };
}
