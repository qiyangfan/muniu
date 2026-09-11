// 价目管理:在线改价(动态价目,metering.md「动态价目」)——列表/编辑/撤销覆盖。
// 价目为运营态数据:改价即时生效零重启;yaml 兜底行编辑即建 DB 覆盖。
import { useCallback, useEffect, useState } from "react";
import { api, type PricingEntry } from "../lib/api";

const EMPTY: PricingEntry = {
  currency: "CNY",
  input: "",
  cached_input: "",
  output: "",
  timezone: "UTC",
  time_windows: [],
};

// 时段窗编辑采用行文本形态(每行「窗口 倍率」),避免内联表单复杂度:
//   16:30-00:30 0.5
function windowsToText(wins: PricingEntry["time_windows"]): string {
  return (wins ?? []).map((w) => `${w.window} ${w.multiplier}`).join("\n");
}
function textToWindows(text: string): { windows: PricingEntry["time_windows"]; bad: boolean } {
  let bad = false;
  const windows = text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "")
    .map((line) => {
      const parts = line.split(/\s+/);
      if (parts.length !== 2) { bad = true; return { window: "", multiplier: "" }; }
      return { window: parts[0], multiplier: parts[1] };
    });
  return { windows, bad };
}

export default function Pricing() {
  const [entries, setEntries] = useState<PricingEntry[]>([]);
  const [err, setErr] = useState("");
  const [editing, setEditing] = useState<string | null>(null); // 正在编辑的 alias(null=列表态)
  const [form, setForm] = useState<PricingEntry>(EMPTY);
  const [winText, setWinText] = useState("");

  const refresh = useCallback(() => {
    api.listPricing()
      .then((r) => { setEntries(r.pricing); setErr(""); })
      .catch((e) => setErr(e.message));
  }, []);
  useEffect(refresh, [refresh]);

  const startEdit = (e: PricingEntry) => {
    setEditing(e.alias ?? "");
    setForm({ ...e, time_windows: e.time_windows ?? [] });
    setWinText(windowsToText(e.time_windows));
  };
  const startNew = () => {
    setEditing("");
    setForm({ ...EMPTY });
    setWinText("");
  };

  const save = async () => {
    const alias = (editing ?? "").trim();
    if (!alias) { setErr("alias 必填"); return; }
    const { windows, bad } = textToWindows(winText);
    if (bad) { setErr("时段窗每行须为「窗口 倍率」两段,如 16:30-00:30 0.5"); return; }
    try {
      await api.putPricing(alias, {
        currency: form.currency ?? "CNY",
        input: form.input ?? "",
        cached_input: form.cached_input ?? "",
        output: form.output ?? "",
        timezone: form.timezone ?? "UTC",
        time_windows: windows,
      });
      setEditing(null);
      setErr("");
      refresh();
    } catch (e2) {
      setErr((e2 as Error).message);
    }
  };

  const remove = async (alias: string) => {
    if (!window.confirm(`撤销 ${alias} 的运行态覆盖?该别名将回落 YAML 静态价。`)) return;
    try {
      await api.deletePricing(alias);
      setErr("");
      refresh();
    } catch (e2) {
      setErr((e2 as Error).message);
    }
  };

  const input = "rounded-md border border-black/10 px-3 py-2";
  if (editing !== null) {
    return (
      <div className="space-y-4">
        {err && <div className="bg-red-50 border border-red-200 rounded-md p-3 text-sm text-red-700">错误:{err}</div>}
        <form
          className="bg-[var(--color-panel)] rounded-xl p-4 space-y-3 text-sm"
          onSubmit={(e) => { e.preventDefault(); void save(); }}
        >
          <div className="flex gap-2 items-center">
            <label className="w-20 text-right text-[var(--color-muted)]">别名</label>
            {/* 新增态须可输入别名(否则新增流程不可用);编辑已有行时锁定 */}
            <input className={input + " flex-1"} value={editing} disabled={editing !== ""} placeholder="model 别名"
              onChange={(e) => setEditing(e.target.value)} />
          </div>
          <div className="flex gap-2 items-center">
            <label className="w-20 text-right text-[var(--color-muted)]">币种</label>
            <select className={input} value={form.currency} onChange={(e) => setForm({ ...form, currency: e.target.value as PricingEntry["currency"] })}>
              <option value="CNY">CNY</option>
              <option value="USD">USD</option>
            </select>
          </div>
          <div className="flex gap-2 items-center">
            <label className="w-20 text-right text-[var(--color-muted)]">输入价</label>
            <input className={input + " w-40"} value={form.input ?? ""} placeholder="0.44"
              onChange={(e) => setForm({ ...form, input: e.target.value })} required />
            <span className="text-[var(--color-muted)]">缓存命中价(空=同输入价)</span>
            <input className={input + " w-32"} value={form.cached_input ?? ""} placeholder="0.014"
              onChange={(e) => setForm({ ...form, cached_input: e.target.value })} />
          </div>
          <div className="flex gap-2 items-center">
            <label className="w-20 text-right text-[var(--color-muted)]">输出价</label>
            <input className={input + " w-40"} value={form.output ?? ""} placeholder="1.32"
              onChange={(e) => setForm({ ...form, output: e.target.value })} required />
            <span className="text-[var(--color-muted)]">时区</span>
            <input className={input + " w-28"} value={form.timezone ?? "UTC"} placeholder="UTC / +08:00"
              onChange={(e) => setForm({ ...form, timezone: e.target.value })} />
          </div>
          <div className="flex gap-2 items-start">
            <label className="w-20 text-right text-[var(--color-muted)] pt-2">时段窗</label>
            <textarea
              className={input + " flex-1 font-mono"}
              rows={3}
              value={winText}
              placeholder={"每行一条:窗口 倍率\n16:30-00:30 0.5"}
              onChange={(e) => setWinText(e.target.value)}
            />
          </div>
          <div className="flex gap-2 justify-end">
            <button type="button" className={input} onClick={() => setEditing(null)}>取消</button>
            <button className="rounded-md bg-[var(--color-brand)] text-white px-4 py-2">保存并生效</button>
          </div>
        </form>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {err && <div className="bg-red-50 border border-red-200 rounded-md p-3 text-sm text-red-700">错误:{err}</div>}
      <div className="flex justify-end">
        <button className="rounded-md bg-[var(--color-brand)] text-white px-4 py-2" onClick={startNew}>新增价目</button>
      </div>
      <table className="w-full bg-[var(--color-panel)] rounded-xl text-sm">
        <thead>
          <tr className="text-left text-[var(--color-muted)]">
            <th className="p-3">别名</th><th className="p-3">来源</th><th className="p-3">币种</th>
            <th className="p-3">输入</th><th className="p-3">缓存命中</th><th className="p-3">输出</th>
            <th className="p-3">时段窗</th><th className="p-3">更新时间</th><th className="p-3"></th>
          </tr>
        </thead>
        <tbody>
          {entries.map((p) => (
            <tr key={p.alias} className="border-t border-black/5">
              <td className="p-3 font-mono">{p.alias}</td>
              <td className="p-3">
                <span className={"px-2 py-0.5 rounded-md text-xs " + (p.source === "db" ? "bg-emerald-100 text-emerald-800" : "bg-gray-100 text-gray-600")}>
                  {p.source === "db" ? "运行态" : "yaml 兜底"}
                </span>
              </td>
              <td className="p-3">{p.currency}</td>
              <td className="p-3 font-mono">{p.input}</td>
              <td className="p-3 font-mono">{p.cached_input || <span className="text-[var(--color-muted)]">=输入</span>}</td>
              <td className="p-3 font-mono">{p.output}</td>
              <td className="p-3 font-mono text-xs">
                {(p.time_windows ?? []).length === 0
                  ? <span className="text-[var(--color-muted)]">—</span>
                  : (p.time_windows ?? []).map((w, i) => <div key={`${w.window}-${i}`}>{w.window} ×{w.multiplier}</div>)}
              </td>
              <td className="p-3 text-xs text-[var(--color-muted)]">{p.updated_at || "—"}</td>
              <td className="p-3">
                <button className="text-[var(--color-brand)] mr-3" onClick={() => p.alias && startEdit({ ...p, alias: p.alias })}>编辑</button>
                {p.source === "db" && (
                  <button className="text-[var(--color-danger)]" onClick={() => p.alias && remove(p.alias)}>撤销覆盖</button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
