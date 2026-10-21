import type * as React from "react";
import type { LucideIcon } from "lucide-react";

import { cn } from "../lib/utils";

/**
 * 空态组件：居中排版，icon 可选；各页面统一使用本组件，不得手写「暂无」样式。
 * 表格内用 <TableCell colSpan={N} className="p-0"> 包裹；独立容器内直接作块级元素。
 */
export function EmptyState({
  icon: Icon,
  children,
  className,
}: {
  icon?: LucideIcon;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-2 py-12 text-sm text-muted-foreground",
        className,
      )}
    >
      {Icon ? <Icon className="size-5" aria-hidden="true" /> : null}
      <div>{children}</div>
    </div>
  );
}
