import * as React from "react";

import { Card } from "./ui/card";
import { cn } from "../lib/utils";

// 本地标准件,API 对齐 kongming(components/stat-card.tsx):
// 统计卡全站单源,label + 数值;内边距 p-4、数值档 text-2xl tabular-nums 由此唯一约定(宪法 Article 6)。
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
    <Card className={cn("p-4", className)}>
      <p className="text-sm text-muted-foreground">{label}</p>
      <p className="mt-1 text-2xl font-semibold tabular-nums">{value}</p>
    </Card>
  );
}
