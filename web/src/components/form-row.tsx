import * as React from "react";

import { cn } from "../lib/utils";

// 本地标准件,API 对齐 kongming(components/form-row.tsx 同位同名):
// 表单字段行全站单源,Label + 控件,纵向间距 gap-1.5 唯一出处(宪法 Article 6)。
// muniu 无 ui/label,Label 用原生元素(控件经 htmlFor/id 关联)。
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
