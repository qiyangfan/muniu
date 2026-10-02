import type * as React from "react";
import type { LucideIcon } from "lucide-react";

import { cn } from "../lib/utils";

/**
 * 空态(全站单源,宪法 Article 6):居中 text-sm text-muted-foreground,py-12 呼吸档,
 * icon 可选;禁静默空白/页面内手搓「暂无」样式。三仓同构(muniu/jinguan 对齐本件)。
 * 表格内配合 TableCell 使用:<TableCell colSpan={N} className="p-0"><EmptyState>…</EmptyState></TableCell>;
 * 独立容器(卡片/弹窗列表)内直接作块级元素使用。
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
