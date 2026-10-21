import { Fragment, type ReactNode } from "react";
import type { LucideIcon } from "lucide-react";

import { cn } from "../lib/utils";
import { Card } from "./ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";
import { EmptyState } from "./empty-state";
import { Skeleton } from "./ui/skeleton";

/** 同一列定义渲染桌面表格和移动卡片；primary、secondary、actions、full 分别控制卡片标题、附加信息、操作行和通栏内容。 */
export type DataCol<T> = {
  key: string;
  label: ReactNode;
  /** 第二个参数表示是否按移动卡片渲染。 */
  render: (row: T, mobile: boolean) => ReactNode;
  /** 桌面表格单元格样式，不用于移动卡片。 */
  cellClassName?: string;
  /** 移动卡片值样式，与桌面单元格样式分开设置。 */
  valueClassName?: string;
  /** 数字和金额列在桌面表格右对齐；移动卡片仍左对齐。 */
  numeric?: boolean;
  /** 桌面 TH title 提示(如 credit 口径) */
  headTitle?: string;
  primary?: boolean;
  secondary?: boolean;
  actions?: boolean;
  full?: boolean;
};

type DataListProps<T> = {
  columns: DataCol<T>[];
  rows: T[];
  rowKey: (row: T) => React.Key;
  empty: ReactNode;
  emptyIcon?: LucideIcon;
  loading?: boolean;
  skeletonRows?: number;
  /** 桌面表格类(如预设目录的 min-w-[1000px]) */
  tableClassName?: string;
  className?: string;
};

export function DataList<T>({
  columns,
  rows,
  rowKey,
  empty,
  emptyIcon,
  loading = false,
  skeletonRows = 3,
  tableClassName,
  className,
}: DataListProps<T>) {
  if (loading) {
    return (
      <div className={cn("space-y-2", className)}>
        {Array.from({ length: skeletonRows }, (_, i) => (
          <Skeleton key={i} className="h-12 w-full" />
        ))}
      </div>
    );
  }
  if (rows.length === 0) {
    return (
      <EmptyState icon={emptyIcon} className={className}>
        {empty}
      </EmptyState>
    );
  }

  // 默认标题列跳过操作列，避免将操作按钮用作卡片标题。
  const primary = columns.find((c) => c.primary) ?? columns.find((c) => !c.actions) ?? columns[0];
  const secondary = columns.find((c) => c.secondary);
  const body = columns.filter((c) => c !== primary && c !== secondary && !c.actions);
  const pairs = body.filter((c) => !c.full);
  const fulls = body.filter((c) => c.full);
  const actionCols = columns.filter((c) => c.actions);

  // 长 token 在窄卡片中换行，避免遮挡相邻字段。
  const field = (c: DataCol<T>, row: T, full: boolean) => (
    <div key={c.key} className={cn("min-w-0", full && "col-span-2")}>
      <dt className="text-xs text-muted-foreground">{c.label}</dt>
      <dd className={cn("mt-0.5 text-sm break-words tabular-nums", c.valueClassName)}>
        {c.render(row, true)}
      </dd>
    </div>
  );

  return (
    <div className={className}>
      <div className="hidden sm:block">
        <Card className="overflow-hidden">
          <Table className={tableClassName}>
            <TableHeader>
              <TableRow>
                {columns.map((c) => (
                  <TableHead
                    key={c.key}
                    title={c.headTitle}
                    className={c.numeric ? "text-right" : undefined}
                  >
                    {c.label}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((row) => (
                <TableRow key={rowKey(row)}>
                  {columns.map((c) => (
                    <TableCell
                      key={c.key}
                      className={cn(c.cellClassName, c.numeric && "text-right tabular-nums")}
                    >
                      {c.actions ? (
                        <div className="flex gap-1.5">{c.render(row, false)}</div>
                      ) : (
                        c.render(row, false)
                      )}
                    </TableCell>
                  ))}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      </div>
      <div className="space-y-3 sm:hidden">
        {rows.map((row) => (
          <Card key={rowKey(row)} className="space-y-3 p-4">
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0 text-sm font-medium break-all">
                {primary.render(row, true)}
              </div>
              {secondary && <div className="shrink-0">{secondary.render(row, true)}</div>}
            </div>
            {(pairs.length > 0 || fulls.length > 0) && (
              <dl className="grid grid-cols-2 gap-x-4 gap-y-3">
                {pairs.map((c) => field(c, row, false))}
                {fulls.map((c) => field(c, row, true))}
              </dl>
            )}
            {actionCols.length > 0 && (
              <div className="flex gap-2 border-t border-border pt-3 [&_button]:min-w-0 [&_button]:flex-1">
                {actionCols.map((c) => (
                  <Fragment key={c.key}>{c.render(row, true)}</Fragment>
                ))}
              </div>
            )}
          </Card>
        ))}
      </div>
    </div>
  );
}
