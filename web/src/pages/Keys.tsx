// 密钥管理:列表/签发(明文仅此一次)/吊销。
import { useCallback, useEffect, useState } from "react";
import { KeyRound } from "lucide-react";
import { api, type KeyRecord } from "../lib/api";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { ErrorAlert, NoticeBanner } from "../components/ui/alert";
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
import { Table, TBody, TD, TH, THead, TR } from "../components/ui/table";
import { Skeleton } from "../components/ui/skeleton";
import { EmptyState } from "../components/empty-state";
import { FormRow } from "../components/form-row";

export default function Keys() {
  const [keys, setKeys] = useState<KeyRecord[]>([]);
  const [plain, setPlain] = useState("");
  const [err, setErr] = useState("");
  const [name, setName] = useState("");
  const [tenant, setTenant] = useState("default");
  const [rpm, setRpm] = useState(60);
  const [busy, setBusy] = useState(false); // 签发防重复
  const [loaded, setLoaded] = useState(false);
  const [revokeTarget, setRevokeTarget] = useState<KeyRecord | null>(null);

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

  return (
    <div className="space-y-4">
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          try {
            const r = await api.createKey(tenant, name, rpm);
            setPlain(r.key);
            setErr("");
            setName("");
            refresh();
          } catch (e2) {
            setErr((e2 as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <FormRow label="租户" htmlFor="key-tenant" className="w-44">
          <Input
            id="key-tenant"
            placeholder="租户"
            value={tenant}
            onChange={(e) => setTenant(e.target.value)}
          />
        </FormRow>
        <FormRow label="key 名称" htmlFor="key-name" className="w-56">
          <Input
            id="key-name"
            placeholder="key 名称"
            value={name}
            onChange={(e) => setName(e.target.value)}
            required
          />
        </FormRow>
        <FormRow label="RPM" htmlFor="key-rpm" className="w-24">
          <Input
            id="key-rpm"
            type="number"
            value={rpm}
            onChange={(e) => setRpm(Number(e.target.value))}
          />
        </FormRow>
        <Button type="submit" disabled={busy}>
          {busy ? "签发中…" : "签发"}
        </Button>
      </form>
      {/* 错误必须独立于明文区展示:混用会让操作者把错误串当 key 复制走 */}
      {err && <ErrorAlert>错误:{err}</ErrorAlert>}
      {plain && (
        <NoticeBanner>
          明文仅此一次: <code className="font-mono">{plain}</code>
        </NoticeBanner>
      )}
      {!loaded ? (
        <div className="space-y-2">
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
        </div>
      ) : (
        <Table>
          <THead>
            <TR>
              <TH>PREFIX</TH>
              <TH>名称</TH>
              <TH>租户</TH>
              <TH>RPM</TH>
              <TH>
                <span className="sr-only">操作</span>
              </TH>
            </TR>
          </THead>
          <TBody>
            {keys.length === 0 ? (
              <TR>
                <TD colSpan={5}>
                  <EmptyState icon={KeyRound}>暂无密钥</EmptyState>
                </TD>
              </TR>
            ) : (
              keys.map((k) => (
                <TR key={k.id}>
                  <TD className="font-mono">{k.prefix}</TD>
                  <TD>{k.name}</TD>
                  <TD>{k.tenant_id}</TD>
                  <TD>{k.rpm_limit}</TD>
                  <TD>
                    <Button
                      variant="ghost-destructive"
                      size="xs"
                      onClick={() => {
                        if (k.prefix) setRevokeTarget(k);
                      }}
                    >
                      吊销
                    </Button>
                  </TD>
                </TR>
              ))
            )}
          </TBody>
        </Table>
      )}

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
