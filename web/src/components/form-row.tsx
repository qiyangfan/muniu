import * as React from "react";

import { cn } from "../lib/utils";

// 本地标准件，API 与 kongming 对齐；通过 htmlFor 将标签关联到控件。
export function FormRow({
  label,
  htmlFor,
  children,
  className,
}: {
  label: string;
  htmlFor?: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("grid gap-1.5", className)}>
      <label htmlFor={htmlFor} className="text-sm leading-none font-medium">
        {label}
      </label>
      {children}
    </div>
  );
}
