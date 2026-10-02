import * as React from "react";

import { cn } from "../lib/utils";

/** 统计卡(全站单源,API/配方对齐 kongming):label + 数值。内边距 p-4、数值档 text-2xl
 *  由此唯一约定(宪法 Article 6);2026-10-02 视觉收敛,原 Card 包装(rounded-xl+shadow-xs)
 *  废止,与双仓同为 rounded-lg + border。 */
export function StatCard({
  label,
  value,
  className,
}: {
  label: React.ReactNode;
  value: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("rounded-lg border bg-card p-4", className)}>
      <p className="text-sm text-muted-foreground">{label}</p>
      <p className="mt-1 text-2xl font-semibold tabular-nums">{value}</p>
    </div>
  );
}
