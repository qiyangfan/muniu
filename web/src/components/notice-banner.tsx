import * as React from "react";
import { AlertCircle } from "lucide-react";

import { cn } from "../lib/utils";

// 普通提示横幅；查询错误使用带 role=alert 的 ErrorAlert。
function NoticeBanner({ className, children, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="notice-banner"
      className={cn(
        "flex items-start gap-2 rounded-md border border-warning/30 bg-warning/5 px-4 py-3 text-sm text-warning",
        className,
      )}
      {...props}
    >
      <AlertCircle className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <div>{children}</div>
    </div>
  );
}

export { NoticeBanner };
