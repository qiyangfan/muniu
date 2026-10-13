// 密钥管理:列表/签发(明文仅此一次)/吊销。
import { useCallback, useEffect, useState } from "react";
import { KeyRound } from "lucide-react";
import { api, type KeyRecord } from "../lib/api";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { ErrorAlert } from "../components/error-alert";
import { NoticeBanner } from "../components/notice-banner";
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
import { Card } from "../components/ui/card";
import { CopyButton } from "../components/copy-button";
import { DataList, type DataCol } from "../components/data-list";
import { FormRow } from "../components/form-row";
import { StatusBadge, type StatusMeta } from "../components/status-badge";
import { formatDateTime, formatNumber } from "../lib/datetime";
import { fmtCredits, rfc3339ToLocal } from "../lib/utils";

// key 状态徽章三态:吊销终态 > 可逆停用 > 启用(与后端判定顺序一致,先吊销后停用)。
const KEY_STATUS_META: StatusMeta = {
  active: { label: "启用", variant: "success" },
  disabled: { label: "已停用", variant: "warning" },
  revoked: { label: "已吊销", variant: "destructive" },
};
const keyStatusOf = (k: KeyRecord): string =>
  k.status === "revoked" ? "revoked" : k.disabled ? "disabled" : "active";

// 快捷过期时刻:点击填入 N 天后的本地时刻(datetime-local 分钟精度)。
// 签发表单与编辑弹窗共用,文案与行为单源。
function QuickExpires({ onPick }: { onPick: (v: string) => void }) {
  const pick = (days: number) => {
    const d = new Date(Date.now() + days * 24 * 60 * 60 * 1000);
    const pad = (n: number) => String(n).padStart(2, "0");
    onPick(
      `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`,
    );
  };
  return (
    <div className="flex flex-wrap gap-2">
      {[1, 7, 30].map((d) => (
        <Button key={d} type="button" variant="outline" size="sm" onClick={() => pick(d)}>
          {d} 天后
        </Button>
      ))}
    </div>
  );
}

export default function Keys() {
  const [keys, setKeys] = useState<KeyRecord[]>([]);
  // 签发结果:明文一次性展示 + Key ID 复制入口。ID 与明文分开承载——锦官绑定填 Key ID,
  // 明文只用于请求鉴权,两者不可互换(openspec fix-api-key-binding-identifiers);
  // 契约 KeyCreateResponse 将 key/id 声明为必返,两值恒在
  const [issued, setIssued] = useState<{ key: string; id: string } | null>(null);
  const [err, setErr] = useState("");
  const [name, setName] = useState("");
  const [tenant, setTenant] = useState("default");
  const [rpm, setRpm] = useState(60);
  const [tpm, setTpm] = useState(0);
  const [quota, setQuota] = useState(0);
  const [budget, setBudget] = useState(0);
  const [expires, setExpires] = useState("");
  const [busy, setBusy] = useState(false); // 签发防重复
  const [loaded, setLoaded] = useState(false);
  const [revokeTarget, setRevokeTarget] = useState<KeyRecord | null>(null);
  const [editTarget, setEditTarget] = useState<KeyRecord | null>(null);
  const [editForm, setEditForm] = useState({ rpm: 0, tpm: 0, quota: 0, budget: 0, expires: "" });

  const refresh = useCallback(() => {
    api
      .listKeys()
      .then((r) => {
        setKeys(r.keys);
        setErr("");
        setLoaded(true);
      })
      .catch((e) => {
        setErr(e.message);
        setLoaded(true);
      });
  }, []);
  useEffect(refresh, [refresh]);

  const revoke = async (prefix: string) => {
    try {
      await api.revokeKey(prefix);
      setErr("");
      refresh();
    } catch (e2) {
      setErr((e2 as Error).message);
    }
  };

  // 启停(可逆):吊销终态的 key 不允许启停操作(按钮层已隐藏,此处防御)
  const toggleDisabled = async (k: KeyRecord) => {
    if (!k.id || k.status === "revoked") return;
    try {
      await api.patchKey(k.id, { disabled: !k.disabled });
      setErr("");
      refresh();
    } catch (e2) {
      setErr((e2 as Error).message);
    }
  };

  const openEdit = (k: KeyRecord) => {
    setErr(""); // 清上次操作残留错误,避免弹窗一打开就显示旧错误
    setEditTarget(k);
    setEditForm({
      rpm: k.rpm_limit ?? 0,
      tpm: k.tpm_limit ?? 0,
      quota: k.daily_quota_credits ?? 0,
      budget: k.max_request_cost_credits ?? 0,
      expires: rfc3339ToLocal(k.expires_at ?? ""),
    });
  };

  const saveEdit = async () => {
    if (!editTarget?.id) return;
    setBusy(true);
    try {
      await api.patchKey(editTarget.id, {
        rpm_limit: editForm.rpm,
        tpm_limit: editForm.tpm,
        daily_quota_credits: editForm.quota,
        max_request_cost_credits: editForm.budget,
        // 输入框清空 = 显式清除过期(null);选值 = 设定新过期(datetime-local 本地时区 → UTC)
        expires_at: editForm.expires === "" ? null : new Date(editForm.expires).toISOString(),
      });
      setErr("");
      setEditTarget(null);
      refresh();
    } catch (e2) {
      setErr((e2 as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const fmtExpires = (v: string | null | undefined) => (v ? formatDateTime(v) : "永不过期");
  // 限额/配额统一口径:0(或空)= 不限,其余数值千分位/credit 缩写
  const fmtLimit = (n: number | null | undefined) => (!n ? "不限" : formatNumber(n));
  const fmtQuota = (n: number | null | undefined) => (!n ? "不限" : fmtCredits(n));

  // 列定义单源:桌面表格与移动卡片共用;mobile 位做触屏口径(租户截短/按钮档位)
  const columns: DataCol<KeyRecord>[] = [
    {
      key: "prefix",
      label: "密钥前缀",
      secondary: true,
      render: (k, mobile) => (
        <span className={mobile ? "font-mono text-xs text-muted-foreground" : "font-mono"}>
          {k.prefix}
        </span>
      ),
    },
    {
      // Key ID 是用量归账/锦官绑定的连接值,与明文(prefix 的来源)和前缀严格区分。
      // 长 ID(UUID 形态)桌面与移动统一截短显示 + title 全值,复制永远取完整原值
      //(design:长 ID 用既有截断/提示组件显示;全值渲染会把表格 min-content 撑爆出横向滚动)
      key: "id",
      label: "Key ID",
      headTitle: "用量归账与锦官绑定使用的真实 ID(不是明文密钥/前缀)",
      render: (k, mobile) =>
        k.id ? (
          <span className="inline-flex items-center gap-1.5">
            <span className={mobile ? "font-mono text-xs" : "font-mono"} title={k.id}>
              {k.id.length > 18 ? `${k.id.slice(0, 16)}…` : k.id}
            </span>
            <CopyButton
              value={k.id}
              label="复制 ID"
              size={mobile ? "sm" : "xs"}
              variant="ghost"
              aria-label={`复制 ${k.name ?? k.prefix ?? ""} 的 Key ID`}
            />
          </span>
        ) : (
          // 兼容条目缺 ID:明确示不可用并禁用复制,不以 prefix/序号充当 ID
          <span className="inline-flex items-center gap-1.5">
            <span className="text-muted-foreground">ID 不可用</span>
            <CopyButton
              value=""
              label="复制 ID"
              disabled
              size={mobile ? "sm" : "xs"}
              variant="ghost"
              aria-label={`复制 ${k.name ?? k.prefix ?? ""} 的 Key ID`}
            />
          </span>
        ),
    },
    { key: "name", label: "名称", primary: true, render: (k) => k.name },
    {
      key: "tenant",
      label: "租户",
      // 租户 id 的 UUID 形态与 Key ID 同款截短(title 保留全值),防表格 min-content 溢出
      render: (k) => {
        const v = k.tenant_id ?? "—";
        return <span title={v}>{v.length > 18 ? `${v.slice(0, 16)}…` : v}</span>;
      },
    },
    {
      key: "rpm",
      label: "RPM",
      numeric: true,
      headTitle: "每分钟请求数;不限 = 未设置上限",
      render: (k) => fmtLimit(k.rpm_limit),
    },
    {
      key: "tpm",
      label: "TPM",
      numeric: true,
      headTitle: "每分钟 Tokens 数;不限 = 未设置上限",
      render: (k) => fmtLimit(k.tpm_limit),
    },
    {
      key: "quota",
      label: "日配额(credit)",
      numeric: true,
      headTitle: "1 credit = 十亿分之一本币",
      render: (k) => fmtQuota(k.daily_quota_credits),
    },
    {
      key: "budget",
      label: "请求预算(credit)",
      numeric: true,
      headTitle: "单请求成本上界:预检拒绝超预算请求,流式超限中断;0 = 不限",
      render: (k) => fmtQuota(k.max_request_cost_credits),
    },
    {
      key: "status",
      label: "状态",
      render: (k) => <StatusBadge meta={KEY_STATUS_META} value={keyStatusOf(k)} />,
    },
    { key: "expires", label: "过期", render: (k) => fmtExpires(k.expires_at) },
    {
      key: "actions",
      label: <span className="sr-only">操作</span>,
      actions: true,
      render: (k, mobile) => (
        <>
          <Button variant="ghost" size={mobile ? "sm" : "xs"} onClick={() => openEdit(k)}>
            编辑
          </Button>
          {k.status !== "revoked" && (
            <Button
              variant="ghost"
              size={mobile ? "sm" : "xs"}
              onClick={() => void toggleDisabled(k)}
            >
              {k.disabled ? "启用" : "停用"}
            </Button>
          )}
          <Button
            variant="ghost-destructive"
            size={mobile ? "sm" : "xs"}
            onClick={() => {
              if (k.prefix) setRevokeTarget(k);
            }}
          >
            吊销
          </Button>
        </>
      ),
    },
  ];

  return (
    <div className="space-y-4">
      {/* 签发表单:移动端单列全宽,sm 起两列、lg 三列、xl 四列——datetime 控件与快捷按钮
          需要约 270px 宽度,六列形态(每列 ~165px)会裁切控件与折乱按钮(走查实测返工) */}
      <Card className="p-4">
        <form
          className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            try {
              const r = await api.createKey(tenant, name, rpm, {
                tpm_limit: tpm,
                daily_quota_credits: quota,
                max_request_cost_credits: budget,
                ...(expires ? { expires_at: new Date(expires).toISOString() } : {}),
              });
              setIssued({ key: r.key, id: r.id });
              setErr("");
              setName("");
              // 新增字段一并复位:残留的过期时刻会静默套到下一把密钥上
              setExpires("");
              setTpm(0);
              setQuota(0);
              setBudget(0);
              refresh();
            } catch (e2) {
              setErr((e2 as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          <FormRow label="租户" htmlFor="key-tenant">
            <Input
              id="key-tenant"
              placeholder="租户"
              value={tenant}
              onChange={(e) => setTenant(e.target.value)}
            />
          </FormRow>
          <FormRow label="key 名称" htmlFor="key-name">
            <Input
              id="key-name"
              placeholder="key 名称"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </FormRow>
          <FormRow label="RPM(每分钟请求数)" htmlFor="key-rpm">
            <Input
              id="key-rpm"
              type="number"
              value={rpm}
              onChange={(e) => setRpm(Number(e.target.value))}
            />
          </FormRow>
          <FormRow label="TPM(每分钟 Tokens 数)" htmlFor="key-tpm">
            <Input
              id="key-tpm"
              type="number"
              value={tpm}
              onChange={(e) => setTpm(Number(e.target.value))}
            />
          </FormRow>
          <FormRow label="日配额(credit)" htmlFor="key-quota">
            <Input
              id="key-quota"
              type="number"
              value={quota}
              onChange={(e) => setQuota(Number(e.target.value))}
            />
          </FormRow>
          <FormRow label="请求预算(credit)" htmlFor="key-budget">
            <Input
              id="key-budget"
              type="number"
              value={budget}
              onChange={(e) => setBudget(Number(e.target.value))}
            />
          </FormRow>
          <FormRow label="过期时刻(本地时间)" htmlFor="key-expires">
            <>
              <Input
                id="key-expires"
                type="datetime-local"
                value={expires}
                onChange={(e) => setExpires(e.target.value)}
              />
              <QuickExpires onPick={setExpires} />
            </>
          </FormRow>
          {/* xl 占两列补齐第二行(配额/过期/签发同排),不留网格空洞 */}
          <div className="flex items-end sm:col-span-2 lg:col-span-3 xl:col-span-2">
            <Button type="submit" disabled={busy} className="w-full sm:w-auto">
              {busy ? "签发中…" : "签发"}
            </Button>
          </div>
        </form>
      </Card>
      <p className="text-xs text-muted-foreground">
        RPM/TPM 与日配额填 0 表示不限制;请求预算填 0 表示不限(超预算的请求会被拒绝或中断流式);
        过期时刻留空表示永不过期;1 credit = 十亿分之一本币。
      </p>
      {/* 错误必须独立于明文区展示:混用会让操作者把错误串当 key 复制走 */}
      {err && <ErrorAlert text={`错误:${err}`} />}
      {issued && (
        <NoticeBanner>
          <div className="space-y-1.5">
            <div>
              明文仅此一次: <code className="break-all font-mono">{issued.key}</code>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <span>
                Key ID: <code className="break-all font-mono">{issued.id}</code>
              </span>
              <CopyButton value={issued.id} label="复制 Key ID" size="xs" variant="outline" />
            </div>
            {/* 锦官绑定入口提示:绑定填 Key ID,明文只用于请求鉴权 */}
            <p className="text-xs">锦官绑定填写 Key ID(不是明文密钥);明文仅用于调用方请求鉴权。</p>
          </div>
        </NoticeBanner>
      )}
      <DataList
        columns={columns}
        rows={keys}
        rowKey={(k) => k.id ?? k.prefix ?? ""}
        empty="暂无密钥"
        emptyIcon={KeyRound}
        loading={!loaded}
        skeletonRows={2}
      />

      <AlertDialog
        open={editTarget !== null}
        onOpenChange={(o) => {
          if (!o) setEditTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>调整限额 — {editTarget?.prefix}</AlertDialogTitle>
            <AlertDialogDescription>
              局部生效:保存后下一请求即用新限额;过期时刻清空 = 永不过期。
            </AlertDialogDescription>
          </AlertDialogHeader>
          {err && <ErrorAlert text={`错误:${err}`} />}
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <FormRow label="RPM(每分钟请求数)" htmlFor="edit-rpm">
              <Input
                id="edit-rpm"
                type="number"
                value={editForm.rpm}
                onChange={(e) => setEditForm({ ...editForm, rpm: Number(e.target.value) })}
              />
            </FormRow>
            <FormRow label="TPM(每分钟 Tokens 数)" htmlFor="edit-tpm">
              <Input
                id="edit-tpm"
                type="number"
                value={editForm.tpm}
                onChange={(e) => setEditForm({ ...editForm, tpm: Number(e.target.value) })}
              />
            </FormRow>
            <FormRow label="日配额(credit)" htmlFor="edit-quota">
              <Input
                id="edit-quota"
                type="number"
                value={editForm.quota}
                onChange={(e) => setEditForm({ ...editForm, quota: Number(e.target.value) })}
              />
            </FormRow>
            <FormRow label="请求预算(credit)" htmlFor="edit-budget">
              <Input
                id="edit-budget"
                type="number"
                value={editForm.budget}
                onChange={(e) => setEditForm({ ...editForm, budget: Number(e.target.value) })}
              />
            </FormRow>
            <FormRow label="过期时刻(本地时间)" htmlFor="edit-expires">
              <>
                <Input
                  id="edit-expires"
                  type="datetime-local"
                  value={editForm.expires}
                  onChange={(e) => setEditForm({ ...editForm, expires: e.target.value })}
                />
                <QuickExpires onPick={(v) => setEditForm({ ...editForm, expires: v })} />
              </>
            </FormRow>
          </div>
          <p className="text-xs text-muted-foreground">
            RPM/TPM 与日配额、请求预算填 0 表示不限制;过期时刻清空 = 永不过期。
          </p>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              disabled={busy}
              onClick={(e) => {
                // 保存失败留在弹窗改错重试(Radix Action 默认点击即关);成功由 saveEdit 内部关闭
                e.preventDefault();
                void saveEdit();
              }}
            >
              {busy ? "保存中…" : "保存"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={revokeTarget !== null}
        onOpenChange={(o) => {
          if (!o) setRevokeTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>吊销密钥</AlertDialogTitle>
            <AlertDialogDescription>
              {revokeTarget ? `确认吊销 ${revokeTarget.prefix} ?吊销后该 key 立即不可用。` : ""}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (!revokeTarget?.prefix) return;
                void revoke(revokeTarget.prefix);
              }}
            >
              吊销
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
