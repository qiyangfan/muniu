// 渠道余额告警:余额源总览、剩余可用天数估算与手动快照录入(manual 源)。
// 估算是网关视角(快照 − 快照后计量消耗,燃速取回看窗口日均),文案一律带「约」。
import { useCallback, useEffect, useState } from "react";
import { api, type BalanceOverview, type BalancesResponse } from "../lib/api";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../components/ui/select";
import { ErrorAlert } from "../components/error-alert";
import { DataList, type DataCol } from "../components/data-list";
import { EmptyState } from "../components/empty-state";
import { Skeleton } from "../components/ui/skeleton";
import { FormRow } from "../components/form-row";
import { formatCreditsExact } from "../lib/utils";
import { formatDateTime } from "../lib/datetime";

// 估算降级原因的用户面文案(实现归因不进正文,只说用户事实)。
const REASON_LABELS: Record<string, string> = {
  no_usage: "无消耗,剩余不限",
  insufficient_data: "消耗样本不足",
  currency_mismatch: "余额币种与本位币不一致,无法估算天数",
  unknown_balance: "余额不可知(不限额或查询未成功)",
  balance_overflow: "余额超出可估算量级,无告警意义",
};

// 剩余小时 → 中文表述,与告警摘要口径一致。
function humanRemaining(hours: number): string {
  if (hours < 0) return "已透支";
  if (hours < 24) return `${hours} 小时`;
  const days = Math.floor(hours / 24);
  const rem = hours % 24;
  if (days >= 7 || rem === 0) return `${days} 天`;
  return `${days} 天 ${rem} 小时`;
}

function estimateText(p: ProviderRow): string {
  if (p.remaining_hours != null) return `约 ${humanRemaining(p.remaining_hours)}`;
  return REASON_LABELS[p.estimate_reason] ?? "无法估算";
}

// 告警规则的用户面标签与严重级视觉映射。
const SEVERITY_VARIANT: Record<string, "destructive" | "warning" | "secondary"> = {
  critical: "destructive",
  warning: "warning",
  info: "secondary",
};

function ruleLabel(ruleKey: string | undefined): string {
  if (!ruleKey) return "告警";
  if (ruleKey === "below") return "低于阈值";
  if (ruleKey === "source_error") return "余额源故障";
  if (ruleKey === "burn_spike") return "消耗突增";
  if (ruleKey.startsWith("cliff:")) return "余额骤降";
  if (ruleKey.startsWith("tier:")) {
    const hours = Number(ruleKey.slice(5));
    return Number.isFinite(hours) ? `剩 ${humanRemaining(hours)}` : ruleKey;
  }
  return ruleKey;
}

// 失败归类的展示标签(last_error 仅影响排障,非用户操作指引)。
const ERROR_LABELS: Record<string, string> = {
  timeout: "查询超时",
  network: "网络失败",
  http: "上游错误",
  parse: "响应异常",
  credential: "凭证被拒",
  upstream: "上游业务错误",
};

// api-types 的非 required 字段均为 optional:取数入口一次归一为必选形态,
// 组件内按非可选消费,避免零散空值守卫漂移。
type ProviderRow = Required<
  Pick<
    BalanceOverview,
    | "provider"
    | "source"
    | "below"
    | "currency"
    | "manual_currency"
    | "burn_per_day_credits"
    | "burn_window_days"
    | "days_with_usage"
    | "estimate_reason"
  >
> & {
  min_effective: string | null;
  remaining_hours: number | null;
  last_checked_at: string;
  keys: {
    key_id: string;
    balance: string | null;
    snapshot: string | null;
    currency: string;
    as_of_at: string;
    consecutive_failures: number;
    last_error: string;
  }[];
  alerts: {
    rule_key: string;
    firing: boolean;
    severity: string;
    last_fired_at: string | null;
  }[];
};

function normalize(r: BalancesResponse): { enabled: boolean; providers: ProviderRow[] } {
  return {
    enabled: r.enabled ?? false,
    providers: (r.providers ?? []).map((p) => ({
      provider: p.provider ?? "",
      source: p.source ?? "",
      below: p.below ?? "",
      currency: p.currency ?? "",
      manual_currency: p.manual_currency ?? "",
      min_effective: p.min_effective ?? null,
      burn_per_day_credits: p.burn_per_day_credits ?? 0,
      burn_window_days: p.burn_window_days ?? 0,
      days_with_usage: p.days_with_usage ?? 0,
      remaining_hours: p.remaining_hours ?? null,
      estimate_reason: p.estimate_reason ?? "",
      last_checked_at: p.last_checked_at ?? "",
      keys: (p.keys ?? []).map((k) => ({
        key_id: k.key_id ?? "",
        balance: k.balance ?? null,
        snapshot: k.snapshot ?? null,
        currency: k.currency ?? "",
        as_of_at: k.as_of_at ?? "",
        consecutive_failures: k.consecutive_failures ?? 0,
        last_error: k.last_error ?? "",
      })),
      alerts: (p.alerts ?? []).map((a) => ({
        rule_key: a.rule_key ?? "",
        firing: a.firing ?? false,
        severity: a.severity ?? "",
        last_fired_at: a.last_fired_at ?? null,
      })),
    })),
  };
}

export default function Balances() {
  const [data, setData] = useState<{ enabled: boolean; providers: ProviderRow[] } | null>(null);
  const [err, setErr] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [actionErr, setActionErr] = useState("");

  const reload = useCallback(() => {
    api
      .balances()
      .then((r) => {
        setData(normalize(r));
        setErr("");
      })
      .catch((e) => setErr(e.message))
      .finally(() => setLoaded(true));
  }, []);

  useEffect(() => {
    reload();
  }, [reload]);

  const providers = data?.providers ?? [];
  const manualProviders = providers.filter((p) => p.source === "manual");

  const refreshOne = async (provider: string) => {
    setActionErr("");
    try {
      await api.balanceRefresh(provider);
      // 查询是异步的:短延迟后重取,错过也无损(下个评估周期兜底)。
      setTimeout(reload, 600);
    } catch (e) {
      setActionErr(e instanceof Error ? e.message : "刷新失败");
    }
  };

  return (
    <div className="space-y-4">
      {manualProviders.length > 0 && <ManualEntry providers={manualProviders} onDone={reload} />}
      {actionErr && <ErrorAlert text={`操作失败:${actionErr}`} />}
      <section className="space-y-3">
        <CardTitle as="h2">渠道余额总览</CardTitle>
        {err ? (
          <ErrorAlert text={`加载失败:${err}`} />
        ) : !loaded ? (
          <Skeleton className="h-48" />
        ) : !data?.enabled ? (
          <EmptyState>
            余额观测未启用:在 muniu.yaml 为渠道声明 balance.source 并重启后生效
          </EmptyState>
        ) : providers.length === 0 ? (
          <EmptyState>暂无声明余额源的渠道</EmptyState>
        ) : (
          <DataList
            tableClassName="min-w-[1080px]"
            columns={columns(refreshOne)}
            rows={providers}
            rowKey={(p) => p.provider}
            empty="暂无声明余额源的渠道"
          />
        )}
      </section>
    </div>
  );
}

// ---------- 手动快照录入(manual 源) ----------

function ManualEntry({ providers, onDone }: { providers: ProviderRow[]; onDone: () => void }) {
  const [picked, setPicked] = useState(providers[0]?.provider ?? "");
  const [amount, setAmount] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  // 选中渠道从列表消失(网关重启移除 manual 源)时渲染期回退首个可用渠道,防失效提交;
  // 派生值不回写状态(effect 内同步 setState 被 react-hooks 禁止),列表恢复后原选中项自然复活。
  const provider = providers.some((p) => p.provider === picked)
    ? picked
    : (providers[0]?.provider ?? "");
  const current = providers.find((p) => p.provider === provider);
  const currency = current?.manual_currency ?? "";

  return (
    <Card>
      <CardHeader>
        <CardTitle as="h2">手动录入余额快照</CardTitle>
        <CardDescription>
          该渠道的余额源无公开查询接口,按控制台实际余额录入;录入后网关按自身计量实时衰减,
          重复录入即覆盖。
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form
          className="grid gap-4 sm:grid-cols-[minmax(0,14rem) minmax(0,1fr) auto] sm:items-end"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setErr("");
            try {
              await api.balanceManual(provider, { amount: amount.trim(), currency });
              setAmount("");
              onDone();
            } catch (e2) {
              setErr(e2 instanceof Error ? e2.message : "录入失败");
            } finally {
              setBusy(false);
            }
          }}
        >
          <FormRow label="渠道">
            <Select value={provider} onValueChange={setPicked}>
              <SelectTrigger aria-label="选择录入渠道" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {providers.map((p) => (
                  <SelectItem key={p.provider} value={p.provider}>
                    {p.provider}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FormRow>
          <FormRow label={`余额(${currency || "币种未知"})`} htmlFor="balance-amount">
            <Input
              id="balance-amount"
              inputMode="decimal"
              placeholder="如 128.50"
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
              required
            />
          </FormRow>
          <Button type="submit" disabled={busy || providers.length === 0}>
            {busy ? "录入中…" : "录入快照"}
          </Button>
        </form>
        {err && (
          <div className="mt-3">
            <ErrorAlert text={err} />
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// ---------- 总览列定义(桌面表格 ↔ 移动卡片单源) ----------

function columns(refreshOne: (provider: string) => void): DataCol<ProviderRow>[] {
  return [
    {
      key: "provider",
      label: "渠道",
      primary: true,
      render: (p) => (
        <>
          <div className="font-medium">{p.provider}</div>
          <div className="mt-0.5">
            <Badge variant="secondary">{p.source}</Badge>
          </div>
        </>
      ),
    },
    {
      key: "min_effective",
      label: "有效余额",
      numeric: true,
      headTitle: "最小有效余额 = 快照 − 快照后计量消耗;币种以渠道为准",
      render: (p) =>
        p.min_effective != null ? (
          <span>
            {p.min_effective}
            {p.currency ? ` ${p.currency}` : ""}
          </span>
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    },
    {
      key: "burn",
      label: "日均消耗",
      numeric: true,
      headTitle: "回看窗口内日均已结算消耗(本位币)",
      render: (p) =>
        p.burn_per_day_credits > 0 ? (
          <span title={`窗口 ${p.burn_window_days} 天,其中 ${p.days_with_usage} 天有消耗`}>
            ≈{formatCreditsExact(p.burn_per_day_credits)}
          </span>
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    },
    {
      key: "remaining",
      label: "剩余估算",
      numeric: true,
      render: (p) => {
        // 紧迫高亮对齐缺省最深档 24h(档位表可配,此处为展示层启发式)
        const critical = p.remaining_hours != null && p.remaining_hours <= 24;
        return (
          <span className={critical ? "font-medium text-destructive" : undefined}>
            {estimateText(p)}
          </span>
        );
      },
    },
    {
      key: "keys",
      label: "各 key 余额",
      full: true,
      render: (p) => (
        <div className="space-y-1">
          {(p.keys ?? []).map((k) => (
            <div key={k.key_id} className="flex flex-wrap items-center gap-x-2 text-xs">
              <span className="font-mono">{k.key_id}</span>
              <span className="tabular-nums">
                {k.balance != null ? `${k.balance} ${k.currency}` : "余额不可知"}
              </span>
              {k.consecutive_failures > 0 && (
                <span className="text-muted-foreground">
                  连续失败 {k.consecutive_failures}
                  {k.last_error ? `(${ERROR_LABELS[k.last_error] ?? k.last_error})` : ""}
                </span>
              )}
            </div>
          ))}
          {(p.keys ?? []).length === 0 && <span className="text-muted-foreground">尚未查询</span>}
        </div>
      ),
    },
    {
      key: "alerts",
      label: "告警",
      render: (p) => {
        const firing = (p.alerts ?? []).filter((a) => a.firing);
        if (firing.length === 0) return <span className="text-muted-foreground">—</span>;
        return (
          <div className="flex flex-wrap gap-1">
            {firing.map((a) => (
              <Badge key={a.rule_key} variant={SEVERITY_VARIANT[a.severity] ?? "secondary"}>
                {ruleLabel(a.rule_key)}
              </Badge>
            ))}
          </div>
        );
      },
    },
    {
      key: "checked",
      label: "最近检查",
      render: (p) => (
        <span className="text-xs text-muted-foreground">
          {p.last_checked_at ? formatDateTime(p.last_checked_at) : "从未"}
        </span>
      ),
    },
    {
      key: "actions",
      label: <span className="sr-only">操作</span>,
      actions: true,
      render: (p, mobile) => (
        <Button
          size={mobile ? "sm" : "xs"}
          variant="outline"
          onClick={() => refreshOne(p.provider)}
          aria-label={`刷新 ${p.provider} 的余额`}
        >
          刷新
        </Button>
      ),
    },
  ];
}
