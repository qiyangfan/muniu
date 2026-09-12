import * as React from "react";
import { AlertCircle } from "lucide-react";

import { cn } from "../../lib/utils";

// 本地标准件,API 对齐 kongming(kongming 走 sonner/query-error;muniu 控制台收敛为两个静态横幅):
// ErrorAlert = 错误态(role=alert,错误禁静默空白/禁 muted 灰);NoticeBanner = warning 提示条。
function ErrorAlert({ className, children, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      role="alert"
      data-slot="error-alert"
      className={cn(
        "flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-4 py-3 text-sm text-destructive",
        className,
      )}
      {...props}
    >
      <AlertCircle className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <div>{children}</div>
    </div>
  );
}

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

export { ErrorAlert, NoticeBanner };
