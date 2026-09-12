import type * as React from "react";
import type { LucideIcon } from "lucide-react";

import { cn } from "../lib/utils";

// 本地标准件,API 对齐 kongming(空态约定形态):居中 text-sm text-muted-foreground,禁静默空白/裸 tbody。
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
