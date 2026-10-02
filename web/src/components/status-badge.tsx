import * as React from "react";

import { Badge } from "./ui/badge";

/** 各域状态 → 展示的映射表(label/variant),由各页面自持、渲染统一走 StatusBadge。 */
export type StatusMeta = Record<
  string,
  { label: string; variant?: React.ComponentProps<typeof Badge>["variant"] }
>;

/**
 * 状态徽章(全站单源,API 对齐 kongming):未知值回落 fallbackVariant(默认 outline)+ 原文,
 * 禁再手写「三元拼 Badge」或各域复制 META 渲染行(宪法 Article 6)。
 * 原语义为「非 X 一律 Y variant」的调用点,必须显式传 fallbackVariant 保持行为等价。
 */
export function StatusBadge({
  meta,
  value,
  fallbackLabel,
  fallbackVariant = "outline",
}: {
  meta: StatusMeta;
  value: string | null | undefined;
  fallbackLabel?: string;
  fallbackVariant?: React.ComponentProps<typeof Badge>["variant"];
}) {
  const m = value ? meta[value] : undefined;
  return (
    <Badge variant={m?.variant ?? fallbackVariant}>
      {m?.label ?? fallbackLabel ?? value ?? "—"}
    </Badge>
  );
}
