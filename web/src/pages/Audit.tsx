import { useEffect, useRef, useState } from "react";
import { api, type AdminAudit } from "../lib/api";
import { DataList, type DataCol } from "../components/data-list";
import { ErrorAlert } from "../components/error-alert";
import { Button } from "../components/ui/button";
import { Card, CardHeader, CardTitle, CardContent } from "../components/ui/card";
import { formatDateTime } from "../lib/datetime";

const RESOURCES: Record<string, string> = {
  keys: "密钥",
  tenants: "租户",
  pricing: "价目",
  "image-pricing": "图片价目",
  admins: "管理员",
  plugins: "插件",
};
const VERBS: Record<string, string> = {
  post: "创建",
  put: "覆盖",
  patch: "更新",
  delete: "删除",
  login: "登录",
  logout: "退出",
};
function actionName(action: string) {
  const [resource, verb] = action.split(".");
  if (resource === "auth") return VERBS[verb] ?? action;
  return `${RESOURCES[resource] ?? resource} · ${VERBS[verb] ?? verb}`;
}

const DETAIL_LABELS: Record<string, string> = {
  username: "用户名",
  password: "密码",
  name: "名称",
  tenant: "租户",
  currency: "币种",
  default: "每张兜底价",
  disabled: "停用",
  enabled: "启用",
  rpm_limit: "每分钟请求限额",
  tpm_limit: "每分钟 Token 限额",
  daily_quota_credits: "日配额",
  max_request_cost_credits: "请求成本预算",
  tiers: "档位价目",
};
function detailText(raw?: string) {
  if (!raw) return "—";
  try {
    // detail 是原始 JSON 字符串；整数用词法值展示，避免解析成 Number 后失真。
    const parsed: unknown = JSON.parse(
      raw,
      (_key: string, value: unknown, context?: { source?: string }) =>
        typeof value === "number"
          ? (context?.source ??
            (Number.isSafeInteger(value) ? String(value) : "数值超出安全显示范围"))
          : value,
    );
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return "—";
    return (
      Object.entries(parsed)
        .map(
          ([key, value]) =>
            `${DETAIL_LABELS[key.toLowerCase()] ?? key}：${value === "<redacted>" ? "已脱敏" : value === "<omitted>" ? "内容已省略" : String(value)}`,
        )
        .join("；") || "—"
    );
  } catch {
    return "详情不可读";
  }
}

export default function Audit() {
  const version = useRef(0);
  const [rows, setRows] = useState<AdminAudit[]>([]);
  const [next, setNext] = useState("");
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState("");
  const load = async (cursor = "") => {
    const current = ++version.current;
    setLoading(true);
    setErr("");
    try {
      const page = await api.audit(cursor);
      if (current !== version.current) return;
      setRows((old) => (cursor ? [...old, ...page.items] : page.items));
      setNext(page.next_cursor);
    } catch (e) {
      if (current === version.current) setErr((e as Error).message);
    } finally {
      if (current === version.current) setLoading(false);
    }
  };
  useEffect(() => {
    void load();
    return () => {
      version.current++;
    };
  }, []);
  const cols: DataCol<AdminAudit>[] = [
    {
      key: "actor",
      label: "操作员",
      primary: true,
      render: (r) => (r.actor_kind === "token" ? "共享管理令牌" : r.actor_name),
    },
    { key: "action", label: "操作", render: (r) => actionName(r.action) },
    {
      key: "outcome",
      label: "结果",
      secondary: true,
      render: (r) =>
        r.outcome === "ok" ? "成功" : r.outcome === "denied" ? "拒绝" : `失败 (${r.outcome})`,
    },
    { key: "time", label: "时间", render: (r) => formatDateTime(r.created_at, { seconds: true }) },
    {
      key: "target",
      label: "目标标识",
      render: (r) => r.target_id || "—",
      cellClassName: "max-w-48 break-all",
    },
    {
      key: "detail",
      label: "操作详情",
      full: true,
      render: (r) => <span className="break-all">{detailText(r.detail)}</span>,
      cellClassName: "max-w-64 whitespace-normal break-all",
    },
  ];
  return (
    <Card>
      <CardHeader>
        <CardTitle>操作审计</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">
          管理变更与登录记录按时间倒序显示。共享令牌记录无法区分个人操作员。
        </p>
        {err && <ErrorAlert text={err} />}
        <Button variant="outline" size="sm" disabled={loading} onClick={() => void load()}>
          刷新
        </Button>
        <DataList
          columns={cols}
          rows={rows}
          rowKey={(r) => r.id}
          empty="暂无操作记录"
          loading={loading && rows.length === 0}
        />
        {next && (
          <Button
            variant="outline"
            className="w-full"
            disabled={loading}
            onClick={() => void load(next)}
          >
            {loading ? "加载中…" : "加载更多"}
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
