import * as React from "react";

import { cn } from "../../lib/utils";

// 本地标准件,API 对齐 kongming;配方逐类对齐 kongming/apps/web 同位文件。
// 密度档原登记「muniu 刻意区分(rounded-xl + shadow-xs + p-4 + 卡题 text-base)」,
// 2026-10-02 owner 裁决全面视觉收敛,自此三仓同配方:rounded-2xl + shadow-xl + p-6 + 卡题 text-lg。
function Card({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card"
      className={cn("rounded-2xl bg-card text-card-foreground shadow-xl", className)}
      {...props}
    />
  );
}

function CardHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-header"
      className={cn("flex flex-col space-y-1.5 p-6", className)}
      {...props}
    />
  );
}

/* 多态标题元素(API 对齐 kongming change 2026-10-02-card-title-heading-semantics):卡片标题
   MUST 以真实标题元素渲染(读屏大纲的卡片级),默认 h3 恒低于页面标题;认证入口等
   「卡题即页内最高级」处传 as="h2"。as 只收标题三档:不收 h1(防页题层级漂移)、
   不收 div(缺陷本体)。 */
function CardTitle({
  as: Component = "h3",
  className,
  ...props
}: React.ComponentProps<"h3"> & { as?: "h2" | "h3" | "h4" }) {
  return (
    <Component
      data-slot="card-title"
      /* text-lg 为 shadcn 默认:页面标题(h2 text-lg/xl)层级必须 ≥ 卡片标题(宪法 Article 7) */
      className={cn("text-lg font-semibold leading-none tracking-tight", className)}
      {...props}
    />
  );
}

function CardDescription({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-description"
      className={cn("text-sm text-muted-foreground", className)}
      {...props}
    />
  );
}

function CardContent({ className, ...props }: React.ComponentProps<"div">) {
  return <div data-slot="card-content" className={cn("p-6 pt-0", className)} {...props} />;
}

export { Card, CardHeader, CardTitle, CardDescription, CardContent };
