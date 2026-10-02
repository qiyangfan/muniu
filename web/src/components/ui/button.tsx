import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { Slot } from "radix-ui";

import { cn } from "../../lib/utils";

/* 等效命中扩展(mobile-experience B1.2):after 伪元素外扩触控热区,视觉尺寸不变;
   各尺寸按视觉高补足 44px 等效(h-8→±1.5、h-9→±1、h-10→±0.5)。xs/icon-xs 例外:
   密集排列(gap-2 级)下外扩以相邻目标间距为上限,±1 防跨目标截胡(spec D5 豁免
   密集行内小按钮);稀疏位需要大热区时在使用处类内覆写 */
const buttonVariants = cva(
  "relative inline-flex shrink-0 items-center justify-center gap-2 rounded-md text-sm font-medium whitespace-nowrap transition-all outline-none after:absolute after:rounded-[inherit] after:content-[''] focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground hover:bg-primary/90",
        destructive:
          "bg-destructive text-white hover:bg-destructive/90 focus-visible:ring-destructive/20 dark:bg-destructive/60 dark:focus-visible:ring-destructive/40",
        outline:
          "border bg-background shadow-xs hover:bg-accent hover:text-accent-foreground dark:border-input dark:bg-input-fill/30 dark:hover:bg-input-fill/50",
        secondary: "bg-secondary text-secondary-foreground hover:bg-secondary/80",
        ghost: "hover:bg-accent hover:text-accent-foreground dark:hover:bg-accent/50",
        /* 行内危险操作(吊销/撤销覆盖)标准变体:收编 variant=ghost + text-destructive 重复覆盖(宪法 Article 2) */
        "ghost-destructive":
          "text-destructive hover:bg-destructive/10 hover:text-destructive dark:hover:bg-destructive/20",
        link: "text-primary underline-offset-4 hover:underline",
      },
      size: {
        default: "h-9 px-4 py-2 has-[>svg]:px-3 after:-inset-1",
        xs: "h-6 gap-1 rounded-md px-2 text-xs has-[>svg]:px-1.5 after:-inset-1 [&_svg:not([class*='size-'])]:size-3",
        sm: "h-8 gap-1.5 rounded-md px-3 has-[>svg]:px-2.5 after:-inset-1.5",
        lg: "h-10 rounded-md px-6 has-[>svg]:px-4 after:-inset-0.5",
        icon: "size-9 after:-inset-1",
        "icon-xs": "size-6 rounded-md after:-inset-1 [&_svg:not([class*='size-'])]:size-3",
        "icon-sm": "size-8 after:-inset-1.5",
        "icon-lg": "size-10 after:-inset-0.5",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  },
);

function Button({
  className,
  variant = "default",
  size = "default",
  asChild = false,
  ...props
}: React.ComponentProps<"button"> &
  VariantProps<typeof buttonVariants> & {
    asChild?: boolean;
  }) {
  const Comp = asChild ? Slot.Root : "button";

  return (
    <Comp
      data-slot="button"
      data-variant={variant}
      data-size={size}
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    />
  );
}

export { Button, buttonVariants };
