// OAuth 订阅账号管理:账号池列表(凭据脱敏)、手动录入(调试通道)、删除(引用校验)、
// 授权发起(consent URL 新窗口打开)。凭据原文任何界面不回显;明文落库事实在页头如实披露。
import { useCallback, useEffect, useState } from "react";
import { KeyRound, Plus, ShieldQuestion } from "lucide-react";
import { api, type OAuthAccountView } from "../lib/api";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { CardTitle } from "../components/ui/card";
import { ErrorAlert } from "../components/error-alert";
import { NoticeBanner } from "../components/notice-banner";
import { DataList, type DataCol } from "../components/data-list";
import { EmptyState } from "../components/empty-state";
import { Skeleton } from "../components/ui/skeleton";
import { FormRow } from "../components/form-row";
import { StatusBadge, type StatusMeta } from "../components/status-badge";
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
import { formatDateTime } from "../lib/datetime";

// 账号状态的用户面标签与视觉映射:expired 是展示推导(行 active 但已过期)。
const ACCOUNT_STATUS: StatusMeta = {
  active: { label: "可用", variant: "success" },
  expired: { label: "凭据已过期(待刷新)", variant: "warning" },
  auth_failed: { label: "授权失效(需重新授权)", variant: "destructive" },
};

export default function OAuthAccounts() {
  const [accounts, setAccounts] = useState<OAuthAccountView[] | null>(null);
  const [flowConfigured, setFlowConfigured] = useState(false);
  const [err, setErr] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [actionErr, setActionErr] = useState("");
  const [notice, setNotice] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<OAuthAccountView | null>(null);

  const reload = useCallback(() => {
    api
      .oauthAccounts()
      .then((r) => {
        setAccounts(r.accounts ?? []);
        setFlowConfigured(r.meta?.flow_configured ?? false);
        setErr("");
      })
      .catch((e) => setErr(e.message))
      .finally(() => setLoaded(true));
  }, []);

  useEffect(() => {
    reload();
  }, [reload]);

  const remove = async (id: string) => {
    setActionErr("");
    try {
      await api.oauthAccountDelete(id);
      setDeleteTarget(null);
      reload();
    } catch (e) {
      setDeleteTarget(null);
      setActionErr(e instanceof Error ? e.message : "删除失败");
    }
  };

  const authorize = async (accountId: string) => {
    setActionErr("");
    setNotice("");
    try {
      const r = await api.oauthAuthorize(accountId);
      window.open(r.authorize_url, "_blank", "noopener");
      setNotice("已打开授权页;完成授权后刷新本页可见最新账号状态");
    } catch (e) {
      setActionErr(e instanceof Error ? e.message : "发起授权失败");
    }
  };

  const rows = accounts ?? [];

  return (
    <div className="space-y-4">
      <section className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle as="h2">订阅账号</CardTitle>
          <CreateEntry onDone={reload} flowConfigured={flowConfigured} onAuthorize={authorize} />
        </div>
        {notice && (
          <NoticeBanner className="border-primary/30 bg-primary/5 text-primary">
            {notice}
          </NoticeBanner>
        )}
        {actionErr && <ErrorAlert text={`操作失败:${actionErr}`} />}
        {err ? (
          <ErrorAlert text={`加载失败:${err}`} />
        ) : !loaded ? (
          <Skeleton className="h-48" />
        ) : rows.length === 0 ? (
          <EmptyState>
            暂无订阅账号:经「录入凭据」或「发起授权」添加,并在 muniu.yaml 以 auth: oauth + account
            引用接入渠道
          </EmptyState>
        ) : (
          <DataList
            tableClassName="min-w-[720px]"
            columns={columns(setDeleteTarget, authorize, flowConfigured)}
            rows={rows}
            rowKey={(a) => a.id}
            empty="暂无订阅账号"
          />
        )}
      </section>

      <AlertDialog
        open={deleteTarget !== null}
        onOpenChange={(o) => {
          if (!o) setDeleteTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除订阅账号 — {deleteTarget?.id}</AlertDialogTitle>
            <AlertDialogDescription>
              删除后引用该账号的渠道将失去凭据来源(需先在配置中移除引用);已产生的用量记录不受影响。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => deleteTarget && remove(deleteTarget.id)}
            >
              删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// ---------- 录入/授权入口 ----------

function CreateEntry({
  onDone,
  flowConfigured,
  onAuthorize,
}: {
  onDone: () => void;
  flowConfigured: boolean;
  onAuthorize: (accountId: string) => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [id, setId] = useState("");
  const [accessToken, setAccessToken] = useState("");
  const [refreshToken, setRefreshToken] = useState("");
  const [expiresAt, setExpiresAt] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setBusy(true);
    setErr("");
    try {
      await api.oauthAccountCreate({
        id: id.trim() || undefined,
        access_token: accessToken.trim(),
        refresh_token: refreshToken.trim(),
        expires_at: expiresAt.trim() || undefined,
      });
      setId("");
      setAccessToken("");
      setRefreshToken("");
      setExpiresAt("");
      setOpen(false);
      onDone();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "录入失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-wrap items-center gap-2">
      {flowConfigured && (
        <Button variant="outline" size="sm" onClick={() => onAuthorize("")}>
          <ShieldQuestion aria-hidden />
          发起授权
        </Button>
      )}
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
        <Plus aria-hidden />
        录入凭据
      </Button>
      <AlertDialog
        open={open}
        onOpenChange={(o) => {
          if (!o) setOpen(false);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>录入订阅账号凭据</AlertDialogTitle>
            <AlertDialogDescription>
              调试通道:直接粘贴凭据对(授权流可用时优先走「发起授权」)。
              凭据以明文保存在本地数据库与备份档中,请确保库文件与备份目录仅本人可读;界面不回显凭据原文。
              过期时刻留空 = 视为已过期,系统会自动换发新凭据。
            </AlertDialogDescription>
          </AlertDialogHeader>
          {err && <ErrorAlert text={`错误:${err}`} />}
          <div className="grid gap-3">
            <FormRow label="账号 ID" htmlFor="oa-id">
              <Input
                id="oa-id"
                placeholder="留空自动生成;如 acc-1"
                value={id}
                onChange={(e) => setId(e.target.value)}
              />
            </FormRow>
            <FormRow label="Access Token" htmlFor="oa-at">
              <Input
                id="oa-at"
                type="password"
                autoComplete="off"
                value={accessToken}
                onChange={(e) => setAccessToken(e.target.value)}
                required
              />
            </FormRow>
            <FormRow label="Refresh Token" htmlFor="oa-rt">
              <Input
                id="oa-rt"
                type="password"
                autoComplete="off"
                value={refreshToken}
                onChange={(e) => setRefreshToken(e.target.value)}
                required
              />
            </FormRow>
            <FormRow label="过期时刻(可选,RFC3339)" htmlFor="oa-exp">
              <Input
                id="oa-exp"
                placeholder="如 2026-11-11T12:00:00Z"
                value={expiresAt}
                onChange={(e) => setExpiresAt(e.target.value)}
              />
            </FormRow>
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              disabled={busy}
              onClick={(e) => {
                e.preventDefault();
                void submit();
              }}
            >
              {busy ? "录入中…" : "录入"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// ---------- 列定义(桌面表格 ↔ 移动卡片单源) ----------

function columns(
  requestRemove: (a: OAuthAccountView) => void,
  authorize: (accountId: string) => Promise<void>,
  flowConfigured: boolean,
): DataCol<OAuthAccountView>[] {
  return [
    {
      key: "id",
      label: "账号",
      primary: true,
      render: (a) => (
        <>
          <div className="font-mono text-sm">{a.id}</div>
          <div className="mt-0.5">
            <Badge variant="secondary">{a.provider_type}</Badge>
          </div>
        </>
      ),
    },
    {
      key: "status",
      label: "状态",
      render: (a) => (
        <div className="space-y-1">
          <StatusBadge meta={ACCOUNT_STATUS} value={a.status} />
          {a.status_reason && (
            <div className="text-xs text-muted-foreground" title={a.status_reason}>
              {a.status_reason}
            </div>
          )}
        </div>
      ),
    },
    {
      key: "access",
      label: "凭据",
      render: (a) => (
        <span className="font-mono text-xs text-muted-foreground">{a.access_token_masked}</span>
      ),
    },
    {
      key: "expires_at",
      label: "过期时刻",
      render: (a) => (
        <span className="text-xs text-muted-foreground">
          {a.expires_at && !a.expires_at.startsWith("0001-")
            ? formatDateTime(a.expires_at)
            : "未设置"}
        </span>
      ),
    },
    {
      key: "actions",
      label: <span className="sr-only">操作</span>,
      actions: true,
      render: (a, mobile) => (
        <div className="flex gap-1.5">
          {flowConfigured && (
            <Button
              size={mobile ? "sm" : "xs"}
              variant="outline"
              onClick={() => void authorize(a.id)}
              aria-label={`重新授权 ${a.id}`}
            >
              <KeyRound aria-hidden />
              授权
            </Button>
          )}
          <Button
            size={mobile ? "sm" : "xs"}
            variant="outline"
            className="text-destructive"
            onClick={() => requestRemove(a)}
            aria-label={`删除 ${a.id}`}
          >
            删除
          </Button>
        </div>
      ),
    },
  ];
}
