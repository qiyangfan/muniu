/* 主题优先级为 localStorage 手动值、系统偏好、亮色。
   与 index.html 的首帧初始化共用 key 和优先级，修改时同步更新。 */
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

/** 按 localStorage 和系统偏好解析当前主题。 */
export function resolveTheme(): Theme {
  return readStored() ?? systemTheme();
}

function applyToDom(theme: Theme): void {
  document.documentElement.classList.toggle("dark", theme === "dark");
}

/** 返回当前主题和手动切换函数。 */
export function useTheme(): { theme: Theme; toggle: () => void } {
  const [theme, setTheme] = useState<Theme>(resolveTheme);

  // 仅在用户没有手动选择主题时跟随系统变化。
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
      /* 存储不可用时，当前会话内仍可切换主题。 */
    }
    applyToDom(next);
    setTheme(next);
  }, []);

  return { theme, toggle };
}
