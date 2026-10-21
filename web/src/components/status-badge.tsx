import * as React from "react";

import { Badge } from "./ui/badge";

/** 各域状态到展示文案和变体的映射，由各页面自持，渲染统一走 StatusBadge。 */
export type StatusMeta = Record<
  string,
  { label: string; variant?: React.ComponentProps<typeof Badge>["variant"] }
>;

/**
 * 状态徽章，API 对齐 kongming：未知值回落到 fallbackVariant（默认 outline）并显示原文。
 * 各页面不得手写三元拼 Badge；需要非 outline 兜底时显式传 fallbackVariant。
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
