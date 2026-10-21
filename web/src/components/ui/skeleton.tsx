import * as React from "react";

import { cn } from "../../lib/utils";

// 本地标准件，API 和配方对齐 kongming。
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
