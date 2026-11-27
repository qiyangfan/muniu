// 运行时页：控制面消费状态、共享控制只读投影、本地命令与最近日志。
// managed 下共享控制只读；本地命令不经锦官队列，锦官不可用仍可执行。
import { useCallback, useEffect, useState } from "react";
import {
  api,
  type RuntimeCommand,
  type RuntimeCommandList,
  type RuntimeLogs,
  type RuntimeStatus,
  type SharedControl,
} from "../lib/api";
import { errorMessage } from "../lib/utils";
import { formatDateTime } from "../lib/datetime";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { Input } from "../components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../components/ui/select";
import { ErrorAlert } from "../components/error-alert";
import { NoticeBanner } from "../components/notice-banner";
import { EmptyState } from "../components/empty-state";
import { StatusBadge, type StatusMeta } from "../components/status-badge";
import { DataList, type DataCol } from "../components/data-list";
import { FormRow } from "../components/form-row";
import { StatCard } from "../components/stat-card";
import { Skeleton } from "../components/ui/skeleton";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "../components/ui/alert-dialog";

const REG_META: StatusMeta = {
  ok: { label: "已注册", variant: "success" },
  degraded: { label: "退避重试", variant: "destructive" },
  starting: { label: "启动中", variant: "secondary" },
  disabled: { label: "已禁用", variant: "secondary" },
};

const CMD_META: StatusMeta = {
  pending: { label: "等待中", variant: "warning" },
  acked: { label: "已回执", variant: "success" },
  failed: { label: "失败", variant: "destructive" },
};

// attrsLine 日志附加值单行化：error_detail 等诊断字段只在 attrs 里，不能丢。
function attrsLine(attrs?: Record<string, string>): string {
  if (!attrs) return "";
  return Object.entries(attrs)
    .map(([k, v]) => `${k}=${v}`)
    .join(" ");
}

type Projection = {
  available?: boolean;
  version?: number;
  tenants?: number;
  pricing?: number;
  revocations_version?: number;
  topology?: { providers?: number; routes?: number };
};

export default function Runtime() {
  const [status, setStatus] = useState<RuntimeStatus | null>(null);
  const [shared, setShared] = useState<SharedControl | null>(null);
  const [commands, setCommands] = useState<RuntimeCommandList | null>(null);
  const [logs, setLogs] = useState<RuntimeLogs | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [err, setErr] = useState("");
  const [action, setAction] = useState<"cache_purge" | "topology_reload">("cache_purge");
  const [range, setRange] = useState<"all" | "tenant">("all");
  const [tenant, setTenant] = useState("");
  const [reason, setReason] = useState("本地控制台执行");
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    return Promise.all([
      api.runtimeStatus(),
      api.runtimeSharedControl(),
      api.runtimeCommands(),
      api.runtimeLogs(50),
    ])
      .then(([s, sc, cmds, lg]) => {
        setStatus(s);
        setShared(sc);
        setCommands(cmds);
        setLogs(lg);
        setErr("");
      })
      .catch((e) => setErr(errorMessage(e)))
      .finally(() => setLoaded(true));
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  const execute = async () => {
    setBusy(true);
    try {
      const params =
        action === "cache_purge"
          ? range === "all"
            ? { all: true }
            : { tenant: tenant.trim() }
          : {};
      await api.runtimeExecuteCommand({ action, params, reason });
      setConfirming(false);
      setErr("");
      await load();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  if (!loaded) return <Skeleton className="h-64 w-full" />;
  if (err && !status) return <ErrorAlert text={err} />;

  const projection = (shared?.shared ?? {}) as Projection;
  const snapshot = status?.snapshot;
  const registration = status?.registration;
  const stats = commands?.stats ?? {};
  const cols: DataCol<RuntimeCommand>[] = [
    {
      key: "created_at",
      label: "时间",
      render: (c) => formatDateTime(c.created_at ?? ""),
    },
    { key: "source", label: "来源", render: (c) => (c.source === "local" ? "本地" : "锦官") },
    { key: "action", label: "动作", primary: true, render: (c) => c.action },
    {
      key: "status",
      label: "状态",
      render: (c) => <StatusBadge meta={CMD_META} value={c.status} />,
    },
    {
      key: "result",
      label: "结果",
      full: true,
      render: (c) => c.result || "—",
    },
  ];

  return (
    <div className="space-y-4">
      {err && <ErrorAlert text={err} />}
      {status?.managed && (
        <NoticeBanner>
          managed
          模式：租户/价目/拓扑/配额/共享策略由锦官管理，本地只读；服务域配置（密钥、供应商、插件、OAuth、余额）仍在本控制台写。
        </NoticeBanner>
      )}
      {snapshot?.stale && snapshot.ready && (
        <NoticeBanner>
          共享控制快照已陈旧{snapshot.last_error ? `（${snapshot.last_error}）` : ""}
          ，网关按 last-known 继续运行。
        </NoticeBanner>
      )}
      {status?.managed && snapshot && !snapshot.ready && (
        <NoticeBanner>
          尚无有效控制快照：租户配额未就绪，数据面已鉴权请求将按 fail-closed 拒绝。
        </NoticeBanner>
      )}

      <div className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
        <span>注册状态</span>
        <StatusBadge meta={REG_META} value={registration?.state} />
        {status?.instance_id && <span className="break-all">实例 {status.instance_id}</span>}
        <Button variant="outline" size="sm" className="ml-auto" onClick={() => void load()}>
          刷新
        </Button>
      </div>
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="运行模式" value={status?.mode ?? "—"} />
        <StatCard label="快照版本" value={snapshot?.version ? String(snapshot.version) : "无"} />
        <StatCard label="注册状态" value={registration?.state ?? "—"} />
        <StatCard
          label="命令（待/成/败）"
          value={`${stats.pending ?? 0} / ${stats.acked ?? 0} / ${stats.failed ?? 0}`}
        />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>共享控制只读投影</CardTitle>
          <CardDescription>计数概览，不含配置明细与凭据</CardDescription>
        </CardHeader>
        <CardContent>
          {!projection.available ? (
            <EmptyState>尚无可用快照</EmptyState>
          ) : (
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
              <StatCard label="租户" value={String(projection.tenants ?? 0)} />
              <StatCard label="价目别名" value={String(projection.pricing ?? 0)} />
              <StatCard
                label="拓扑（供应商/路由）"
                value={`${projection.topology?.providers ?? 0} / ${projection.topology?.routes ?? 0}`}
              />
              <StatCard label="吊销名单版本" value={String(projection.revocations_version ?? 0)} />
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>本地运行命令</CardTitle>
          <CardDescription>直接在本进程执行并审计，不经锦官队列</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <FormRow label="动作" htmlFor="runtime-action">
              <Select value={action} onValueChange={(v) => setAction(v as typeof action)}>
                <SelectTrigger id="runtime-action" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="cache_purge">清空语义缓存</SelectItem>
                  {!status?.managed && (
                    <SelectItem value="topology_reload">重载配置拓扑（仅 standalone）</SelectItem>
                  )}
                </SelectContent>
              </Select>
            </FormRow>
            {action === "cache_purge" && (
              <FormRow label="清理范围" htmlFor="runtime-range">
                <Select value={range} onValueChange={(v) => setRange(v as typeof range)}>
                  <SelectTrigger id="runtime-range" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">全部租户</SelectItem>
                    <SelectItem value="tenant">指定租户</SelectItem>
                  </SelectContent>
                </Select>
              </FormRow>
            )}
            {action === "cache_purge" && range === "tenant" && (
              <FormRow label="租户 ID" htmlFor="runtime-tenant">
                <Input
                  id="runtime-tenant"
                  value={tenant}
                  onChange={(e) => setTenant(e.target.value)}
                  placeholder="租户 id"
                />
              </FormRow>
            )}
            <FormRow label="原因（审计留痕）" htmlFor="runtime-reason">
              <Input
                id="runtime-reason"
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
            </FormRow>
          </div>
          <Button
            onClick={() => setConfirming(true)}
            disabled={action === "cache_purge" && range === "tenant" && !tenant.trim()}
            className="w-full sm:w-auto"
          >
            执行命令
          </Button>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>命令台账</CardTitle>
          <CardDescription>锦官跨服务命令与本地命令的最近 50 条</CardDescription>
        </CardHeader>
        <CardContent>
          <DataList
            columns={cols}
            rows={commands?.commands ?? []}
            rowKey={(c) => c.id}
            empty={<EmptyState>暂无命令记录</EmptyState>}
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>最近日志</CardTitle>
          <CardDescription>进程内环形缓冲，敏感键已脱敏</CardDescription>
        </CardHeader>
        <CardContent>
          {(logs?.entries ?? []).length === 0 ? (
            <EmptyState>暂无日志</EmptyState>
          ) : (
            <ul className="max-h-80 space-y-1 overflow-auto font-mono text-xs">
              {(logs?.entries ?? []).map((e, i) => (
                <li key={`${e.time}-${i}`} className="flex gap-2">
                  <span className="shrink-0 text-muted-foreground">
                    {formatDateTime(e.time ?? "")}
                  </span>
                  <span className="shrink-0 uppercase text-muted-foreground">{e.level}</span>
                  <span className="min-w-0 break-all">
                    {e.message}
                    {attrsLine(e.attrs) && (
                      <span className="block text-muted-foreground">{attrsLine(e.attrs)}</span>
                    )}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>

      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认执行本地命令</AlertDialogTitle>
            <AlertDialogDescription>
              {action === "cache_purge"
                ? `将清空${range === "all" ? "全部租户" : `租户 ${tenant}`}的语义缓存，操作立即生效且记录审计。`
                : "将按配置文件重载拓扑；managed 模式下该动作会被拒绝。"}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction variant="destructive" disabled={busy} onClick={() => void execute()}>
              {busy ? "执行中…" : "确认执行"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
