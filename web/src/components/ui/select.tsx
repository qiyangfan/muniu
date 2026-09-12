import * as React from "react";
import { ChevronDown } from "lucide-react";

import { cn } from "../../lib/utils";

// 本地标准件,API 对齐 kongming(kongming 为 Radix Select 组合式;muniu 下拉仅 CNY/USD 单处,
// 收敛为原生 select 套 input 同族配方,交互态四件套一致)。
// appearance-none 去系统箭头后必须补 chevron 指示符,否则视觉上与输入框不可区分
function Select({ className, children, ...props }: React.ComponentProps<"select">) {
  return (
    <div className="relative w-full" data-slot="select">
      <select
        className={cn(
          "flex h-9 w-full appearance-none rounded-md border border-input bg-transparent pr-8 pl-3 py-2 text-sm whitespace-nowrap shadow-xs transition-[color,box-shadow] outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20",
          className,
        )}
        {...props}
      >
        {children}
      </select>
      <ChevronDown
        aria-hidden
        className="pointer-events-none absolute top-1/2 right-3 size-4 -translate-y-1/2 text-muted-foreground"
      />
    </div>
  );
}

export { Select };
