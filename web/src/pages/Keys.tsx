// 密钥管理:列表/签发(明文仅此一次)/吊销。
import { useCallback, useEffect, useState } from "react";
import { api, type KeyRecord } from "../lib/api";

export default function Keys() {
  const [keys, setKeys] = useState<KeyRecord[]>([]);
  const [plain, setPlain] = useState("");
  const [err, setErr] = useState("");
  const [name, setName] = useState("");
  const [tenant, setTenant] = useState("default");
  const [rpm, setRpm] = useState(60);

  const refresh = useCallback(() => {
    api.listKeys()
      .then((r) => { setKeys(r.keys); setErr(""); })
      .catch((e) => setErr(e.message));
  }, []);
  useEffect(refresh, [refresh]);

  // 错误必须独立于明文区展示:混用会让操作者把错误串当 key 复制走
  const errBox = err && (
    <div className="bg-red-50 border border-red-200 rounded-md p-3 text-sm text-red-700">错误:{err}</div>
  );

  return (
    <div className="space-y-4">
      <form
        className="flex gap-2 items-end"
        onSubmit={async (e) => {
          e.preventDefault();
          try {
            const r = await api.createKey(tenant, name, rpm);
            setPlain(r.key);
            setErr("");
            setName("");
            refresh();
          } catch (e2) {
            setErr((e2 as Error).message);
          }
        }}
      >
        <input className="rounded-md border border-black/10 px-3 py-2" placeholder="租户" value={tenant} onChange={(e) => setTenant(e.target.value)} />
        <input className="rounded-md border border-black/10 px-3 py-2" placeholder="key 名称" value={name} onChange={(e) => setName(e.target.value)} required />
        <input className="rounded-md border border-black/10 px-3 py-2 w-24" type="number" value={rpm} onChange={(e) => setRpm(Number(e.target.value))} />
        <button className="rounded-md bg-[var(--color-brand)] text-white px-4 py-2">签发</button>
      </form>
      {errBox}
      {plain && (
        <div className="bg-amber-50 border border-amber-200 rounded-md p-3 text-sm">
          明文仅此一次: <code className="font-mono">{plain}</code>
        </div>
      )}
      <table className="w-full bg-[var(--color-panel)] rounded-xl text-sm">
        <thead>
          <tr className="text-left text-[var(--color-muted)]">
            <th className="p-3">PREFIX</th><th className="p-3">名称</th><th className="p-3">租户</th><th className="p-3">RPM</th><th className="p-3"></th>
          </tr>
        </thead>
        <tbody>
          {keys.map((k) => (
            <tr key={k.id} className="border-t border-black/5">
              <td className="p-3 font-mono">{k.prefix}</td>
              <td className="p-3">{k.name}</td>
              <td className="p-3">{k.tenant_id}</td>
              <td className="p-3">{k.rpm_limit}</td>
              <td className="p-3">
                <button
                  className="text-[var(--color-danger)]"
                  onClick={async () => {
                    if (!k.prefix || !window.confirm(`确认吊销 ${k.prefix} ?吊销后该 key 立即不可用。`)) return;
                    try {
                      await api.revokeKey(k.prefix);
                      setErr("");
                      refresh();
                    } catch (e2) {
                      setErr((e2 as Error).message);
                    }
                  }}
                >
                  吊销
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
