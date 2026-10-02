import * as React from "react";

import { cn } from "../../lib/utils";

// 本地标准件,API/配方对齐 kongming(bg-accent 弱底;2026-10-02 视觉收敛,原 bg-muted 密档分叉废止)
function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="skeleton"
      className={cn("animate-pulse rounded-md bg-accent", className)}
      {...props}
    />
  );
}

export { Skeleton };
