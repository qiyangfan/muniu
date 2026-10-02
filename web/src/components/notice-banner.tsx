import * as React from "react";
import { AlertCircle } from "lucide-react";

import { cn } from "../lib/utils";

// 提示横幅(muniu 控制台特有件,三仓骨架词汇表中的 NoticeBanner,kongming/jinguan
// 暂无消费方):warning 弱底提示条,与 ErrorAlert(role=alert,错误禁静默)语义分立。
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
