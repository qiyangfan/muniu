import * as React from "react";

import { cn } from "../../lib/utils";

/* shadcn v4 默认配方(rounded-md + ring-[3px] + aria-invalid 态),API/配方逐类对齐
   kongming/apps/web 同位文件;禁改回旧版 rounded-lg/ring-2(前端宪法 Article 2)。
   size="sm"(h-8)与 SelectTrigger sm 同档,供紧凑筛选行,业务禁再写 h-7/h-8 高度覆盖。
   日期/时间族修正(2026-10-02 实拍归因,三仓单源在本配方):配方基类的 display:flex
   会让 Chromium 把 datetime-local 的分段与日历指示器当 flex 项从左随内容排布(指示器
   悬在内容后而非右贴边),这类控件回退 block 恢复原生态 */
const DATE_FAMILY_TYPES = new Set(["date", "datetime-local", "time", "month", "week"]);

function Input({
  className,
  type,
  size = "default",
  ...props
}: Omit<React.ComponentProps<"input">, "size"> & { size?: "default" | "sm" }) {
  return (
    <input
      type={type}
      data-slot="input"
      data-size={size}
      className={cn(
        type != null && DATE_FAMILY_TYPES.has(type) ? "block" : "flex",
        "h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-base shadow-xs transition-[color,box-shadow] outline-none file:border-0 file:bg-transparent file:text-sm file:font-medium placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 md:text-sm dark:bg-input-fill/30 dark:aria-invalid:ring-destructive/40 data-[size=sm]:h-8",
        className,
      )}
      {...props}
    />
  );
}

export { Input };
