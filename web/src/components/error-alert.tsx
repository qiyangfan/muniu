/* 查询错误态共享组件(宪法 Article 7 正名 ErrorAlert;kongming 单源,三仓同名同配方)。
   查询失败显式渲染 role=alert(不伪装空态/不静默空白);新页面一律
   复用本组件,不得各自手写错误分支或省略错误态(web-nav spec 立约);
   表单/对话框内的提交类行内反馈不属本件管辖(spec「服务端错误行内反馈」)。
   2026-10-02 视觉收敛:原 muniu ui/alert.tsx 的 children 型同名件废止,
   以本件 text 单参 API 为准(三仓对齐登记)。 */

import { AlertCircle } from "lucide-react";

export function ErrorAlert({ text = "加载失败,请稍后重试" }: { text?: string }) {
  return (
    <div
      role="alert"
      className="flex items-center gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-4 py-3 text-sm text-destructive"
    >
      <AlertCircle className="size-4 shrink-0" aria-hidden />
      <span>{text}</span>
    </div>
  );
}
