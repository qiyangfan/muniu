import * as React from "react";

import { cn } from "../../lib/utils";

// 本地标准件,API 对齐 kongming;kongming 用 bg-accent,muniu 密档收编为 bg-muted(弱底同族)
function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="skeleton"
      className={cn("animate-pulse rounded-md bg-muted", className)}
      {...props}
    />
  );
}

export { Skeleton };
