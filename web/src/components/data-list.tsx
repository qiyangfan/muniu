import { Fragment, type ReactNode } from "react";
import type { LucideIcon } from "lucide-react";

import { cn } from "../lib/utils";
import { Card } from "./ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";
import { EmptyState } from "./empty-state";
import { Skeleton } from "./ui/skeleton";

/**
 * 数据列表标准件(业务层):一套列定义驱动两种形态,四页共用单源。
 * sm+ 沿用 ui/Table 契约(卡片底+圆角+横向滚动);窄屏渲染为实体卡片堆叠——
 * 移动端表格横向平移是「桌面缩小」反模式(列越多越不可用),须按卡片范式重排。
 * 列角色(仅影响移动卡片):primary=卡片标题(缺省首列)/secondary=标题行右侧
 * (徽章/ID 等自解释信息,不重复渲染 label)/actions=底部操作行(整行等宽,
 * 触屏友好)/full=独占整行的长内容(base_url/时段窗/说明);其余字段两列网格。
 * render 第二参 mobile:按触屏口径切换按钮档位(卡片操作行用 sm,桌面表格用 xs)。
 */
export type DataCol<T> = {
  key: string;
  label: ReactNode;
  render: (row: T, mobile: boolean) => ReactNode;
  /** 桌面表格 TD 类(mono/列宽/break-all);不透传移动端 */
  cellClassName?: string;
  /** 移动卡片 dd 类;与 cellClassName 分离,避免桌面列宽工具类污染卡片 */
  valueClassName?: string;
  /** 数字/金额列:桌面表格右对齐 + 等宽数字(逐行对账口径,TH 随数据同侧);
   *  移动卡片保持 label-above-value 左对齐范式,等宽数字由 dd 全局类承担 */
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

  // 主字段兜底跳过操作列:操作列 render 输出按钮,落到标题位会渲染成按钮卡片
  const primary = columns.find((c) => c.primary) ?? columns.find((c) => !c.actions) ?? columns[0];
  const secondary = columns.find((c) => c.secondary);
  const body = columns.filter((c) => c !== primary && c !== secondary && !c.actions);
  const pairs = body.filter((c) => !c.full);
  const fulls = body.filter((c) => c.full);
  const actionCols = columns.filter((c) => c.actions);

  // 卡片字段对:label + 值;tabular-nums 让数字列对齐,文本列不受影响;
  // break-words 兜底不可断长 token(环境变量名/URL):min-w-0 限宽后任其溢出会涂进相邻列形成遮挡
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
      {/* 桌面:ui/Table 契约(卡片底+圆角+横向滚动;容器收编于本件,业务侧不再逐页包裹) */}
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
      {/* 移动:实体卡片堆叠;呼吸节奏统一 12px 档(卡间/字段行距/操作分隔),拥挤即返工此处 */}
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
