import * as React from "react";

import { cn } from "../../lib/utils";

// 本地标准件,API 对齐 kongming(kongming 名为 TableHeader/TableRow/TableHead/TableCell);
// 密档配方:行 border-t、TH/TD p-3。三页表格同构「卡片底 + 圆角 + 横向滚动」,
// 按宪法 Article 2 收编进容器默认,业务侧不再逐页包裹。
function Table({ className, ...props }: React.ComponentProps<"table">) {
  return (
    <div data-slot="table-container" className="relative w-full overflow-x-auto rounded-xl bg-card">
      <table data-slot="table" className={cn("w-full text-sm", className)} {...props} />
    </div>
  );
}

function THead({ className, ...props }: React.ComponentProps<"thead">) {
  return <thead data-slot="table-head" className={cn("[&_tr]:border-t-0", className)} {...props} />;
}

function TBody({ className, ...props }: React.ComponentProps<"tbody">) {
  return (
    <tbody
      data-slot="table-body"
      className={cn("[&_tr:last-child]:border-0", className)}
      {...props}
    />
  );
}

function TR({ className, ...props }: React.ComponentProps<"tr">) {
  return (
    <tr
      data-slot="table-row"
      className={cn("border-t border-border transition-colors hover:bg-muted/50", className)}
      {...props}
    />
  );
}

function TH({ className, ...props }: React.ComponentProps<"th">) {
  return (
    <th
      data-slot="table-head-cell"
      className={cn(
        "p-3 text-left align-middle font-normal whitespace-nowrap text-muted-foreground",
        className,
      )}
      {...props}
    />
  );
}

function TD({ className, ...props }: React.ComponentProps<"td">) {
  return (
    <td
      data-slot="table-cell"
      className={cn("p-3 align-middle whitespace-nowrap", className)}
      {...props}
    />
  );
}

export { Table, THead, TBody, TR, TH, TD };
