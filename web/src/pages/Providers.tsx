// 供应商健康:只读拓扑 + key 池三态(healthy/cooldown/disabled)。
import { useEffect, useState } from "react";
import { api, type ProviderInfo } from "../lib/api";

const STATE_COLOR: Record<string, string> = {
  healthy: "bg-emerald-100 text-emerald-800",
  cooldown: "bg-amber-100 text-amber-800",
  disabled: "bg-red-100 text-red-800",
  unknown: "bg-gray-100 text-gray-600",
};

export default function Providers() {
  const [providers, setProviders] = useState<ProviderInfo[]>([]);
  const [err, setErr] = useState("");
  useEffect(() => {
    api.providers()
      .then((r) => { setProviders(r.providers); setErr(""); })
      .catch((e) => setErr(e.message));
  }, []);
  if (err) {
    return <div className="bg-red-50 border border-red-200 rounded-md p-3 text-sm text-red-700">加载失败:{err}</div>;
  }
  return (
    <div className="grid gap-4 md:grid-cols-2">
      {providers.map((p) => (
        <div key={p.name} className="bg-[var(--color-panel)] rounded-xl p-4 space-y-2">
          <div className="flex justify-between">
            <span className="font-semibold">{p.name}</span>
            <span className="text-xs text-[var(--color-muted)]">{p.adapter}</span>
          </div>
          <div className="text-xs text-[var(--color-muted)] break-all">{p.base_url}</div>
          <div className="text-xs">weight={p.weight} priority={p.priority}</div>
          <div className="flex flex-wrap gap-2">
            {(p.keys ?? []).map((k) => (
              <span key={k.id} className={"px-2 py-0.5 rounded-md text-xs " + (STATE_COLOR[k.state ?? "unknown"])}>
                {k.id}:{k.state}
              </span>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}
