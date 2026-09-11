import { useState } from "react";
import { api, getToken, setToken } from "./lib/api";
import Overview from "./pages/Overview";
import Keys from "./pages/Keys";
import Usage from "./pages/Usage";
import Providers from "./pages/Providers";
import Pricing from "./pages/Pricing";

// 登录 = 输入 admin token,先经一次真实请求校验再进入(否则无效 token 表现为各页静默空白)
function Login({ onOk }: { onOk: () => void }) {
  const [v, setV] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <div className="min-h-screen grid place-items-center bg-[var(--color-bg)]">
      <form
        className="bg-[var(--color-panel)] rounded-xl p-8 shadow w-96 space-y-4"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setErr("");
          setToken(v.trim());
          try {
            await api.listTenants(); // 廉价校验:token 无效在此拦截,不放进入主界面
            onOk();
          } catch (e2) {
            setToken("");
            setErr(`token 无效:${(e2 as Error).message}`);
          } finally {
            setBusy(false);
          }
        }}
      >
        <h1 className="text-xl font-semibold">木牛控制台</h1>
        <input
          className="w-full rounded-md border border-black/10 px-3 py-2 focus:outline-2 outline-[var(--color-brand)]"
          placeholder="粘贴 MUNIU_ADMIN_TOKEN"
          value={v}
          onChange={(e) => setV(e.target.value)}
          type="password"
        />
        {err && <div className="text-sm text-red-600">{err}</div>}
        <button
          className="w-full rounded-md bg-[var(--color-brand)] text-white py-2 hover:opacity-90 disabled:opacity-50"
          disabled={busy}
        >
          {busy ? "校验中…" : "登录"}
        </button>
      </form>
    </div>
  );
}

const TABS = [
  { key: "overview", label: "总览" },
  { key: "keys", label: "密钥管理" },
  { key: "usage", label: "用量明细" },
  { key: "providers", label: "供应商健康" },
  { key: "pricing", label: "价目管理" },
] as const;

export default function App() {
  const [logged, setLogged] = useState(getToken() !== "");
  const [tab, setTab] = useState<(typeof TABS)[number]["key"]>("overview");
  if (!logged) return <Login onOk={() => setLogged(true)} />;

  return (
    <div className="min-h-screen">
      <header className="flex items-center gap-6 px-6 h-14 bg-[var(--color-panel)] border-b border-black/5">
        <span className="font-semibold text-[var(--color-brand)]">木牛 muniu</span>
        <nav className="flex gap-4">
          {TABS.map((t) => (
            <button
              key={t.key}
              onClick={() => setTab(t.key)}
              className={
                "px-3 py-1.5 rounded-md " +
                (tab === t.key ? "bg-[var(--color-brand)] text-white" : "text-[var(--color-muted)] hover:text-[var(--color-ink)]")
              }
            >
              {t.label}
            </button>
          ))}
        </nav>
        <button
          className="ml-auto text-sm text-[var(--color-muted)] hover:text-[var(--color-danger)]"
          onClick={() => {
            setToken("");
            setLogged(false);
          }}
        >
          退出
        </button>
      </header>
      <main className="p-6 max-w-6xl mx-auto">
        {tab === "overview" && <Overview />}
        {tab === "keys" && <Keys />}
        {tab === "usage" && <Usage />}
        {tab === "providers" && <Providers />}
        {tab === "pricing" && <Pricing />}
      </main>
    </div>
  );
}
