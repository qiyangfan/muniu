// 优先使用 Clipboard API，不可用时回退 execCommand；显示可截断但复制始终使用完整原值。
// 复制失败时保留可手动选择的文本。
import { useEffect, useRef, useState, type ComponentProps } from "react";
import { Check, Copy } from "lucide-react";

import { Button } from "./ui/button";

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try {
      ok = document.execCommand("copy");
    } catch {
      ok = false;
    }
    ta.remove();
    return ok;
  }
}

type CopyButtonProps = {
  /** 复制的原始完整值(与展示文本解耦:展示可截断,复制不截断) */
  value: string;
  /** 按钮文案 */
  label: string;
  /** 复制成功的短暂反馈文案 */
  copiedLabel?: string;
  /** 复制失败提示(值保持可手动选中) */
  failHint?: string;
} & Pick<ComponentProps<typeof Button>, "variant" | "size" | "disabled" | "aria-label">;

export function CopyButton({
  value,
  label,
  copiedLabel = "已复制",
  failHint = "复制失败,请手动复制",
  variant = "outline",
  size,
  disabled,
  "aria-label": ariaLabel,
}: CopyButtonProps) {
  const [copied, setCopied] = useState(false);
  const [failed, setFailed] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  // 缺值占位由调用方展示。
  const copy = async () => {
    window.clearTimeout(timer.current);
    setFailed(false);
    if (await copyText(value)) {
      setCopied(true);
      timer.current = window.setTimeout(() => setCopied(false), 2000);
    } else {
      setCopied(false);
      setFailed(true);
    }
  };
  return (
    <span className="inline-flex items-center gap-1.5">
      <Button
        type="button"
        size={size}
        variant={variant}
        disabled={disabled}
        aria-label={ariaLabel}
        onClick={() => void copy()}
      >
        {copied ? (
          <>
            <Check className="size-3.5" aria-hidden /> {copiedLabel}
          </>
        ) : (
          <>
            <Copy className="size-3.5" aria-hidden /> {label}
          </>
        )}
      </Button>
      {failed && (
        <span role="alert" className="text-xs text-destructive">
          {failHint}
        </span>
      )}
    </span>
  );
}
