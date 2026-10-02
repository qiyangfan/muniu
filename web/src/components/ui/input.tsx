import * as React from "react";

import { cn } from "../../lib/utils";

// 本地标准件,API 对齐 kongming(shadcn v4 配方:rounded-md + ring-[3px] + aria-invalid 态,与 button/select/textarea 同族)。
// 日期/时间族修正:配方基类的 display:flex 会让 Chromium 把 datetime-local 的分段与日历指示器
// 当 flex 项从左随内容排布(指示器悬在内容后而非右贴边),这类控件回退 block 恢复原生态。
// kongming 同源 input 存在相同潜在问题,双仓对齐时以此为准。
function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  const dateFamily =
    type === "date" ||
    type === "datetime-local" ||
    type === "time" ||
    type === "month" ||
    type === "week";
  return (
    <input
      type={type}
      data-slot="input"
      className={cn(
        dateFamily ? "block" : "flex",
        "h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-base shadow-xs transition-[color,box-shadow] outline-none file:border-0 file:bg-transparent file:text-sm file:font-medium placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 md:text-sm",
        className,
      )}
      {...props}
    />
  );
}

export { Input };
