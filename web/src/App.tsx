import { useState } from "react";
import { Moon, Sun } from "lucide-react";
import { api, ApiError, getToken, setToken } from "./lib/api";
import { useTheme } from "./lib/theme";
import { Button } from "./components/ui/button";
import { Input } from "./components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "./components/ui/card";
import { ErrorAlert } from "./components/ui/alert";
import { FormRow } from "./components/form-row";
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
    <div className="min-h-screen grid place-items-center bg-background px-4">
      <Card className="w-full max-w-96">
        <CardHeader>
          {/* 登录页标题为页面级,压过 CardTitle 密档默认(宪法 Article 7:页标题 ≥ 卡片标题) */}
          <CardTitle className="text-xl">木牛控制台</CardTitle>
        </CardHeader>
        <CardContent>
          <form
            className="grid gap-4"
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
                setErr(
                  e2 instanceof ApiError && e2.status === 401
                    ? "管理员令牌无效或已过期,请重新登录"
                    : `错误:${e2 instanceof Error ? e2.message : "登录失败,请稍后重试"}`,
                );
              } finally {
                setBusy(false);
              }
            }}
          >
            <FormRow label="管理员令牌" htmlFor="admin-token">
              <Input
                id="admin-token"
                type="password"
                placeholder="粘贴管理员令牌"
                value={v}
                onChange={(e) => setV(e.target.value)}
              />
            </FormRow>
            {err && <ErrorAlert>{err}</ErrorAlert>}
            <Button type="submit" className="w-full" disabled={busy}>
              {busy ? "校验中…" : "登录"}
            </Button>
          </form>
        </CardContent>
      </Card>
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
  const { theme, toggle } = useTheme();
  if (!logged) return <Login onOk={() => setLogged(true)} />;

  return (
    <div className="min-h-screen">
      {/* 多屏协同(宪法 Article 9):窄屏头部两行折行(品牌+主操作 / 导航),md+ 恢复单行 */}
      <header className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2 bg-card border-b border-border md:h-14 md:flex-nowrap md:gap-6 md:px-6 md:py-0">
        <span className="font-semibold text-primary">木牛 muniu</span>
        {/* nav 自身也要可折行:5 个 tab 在 375px 下超出单行,按钮 shrink-0 不可收缩(Article 9 实测修复) */}
        <nav className="flex flex-wrap gap-1.5 md:flex-nowrap md:gap-4">
          {TABS.map((t) => (
            <Button
              key={t.key}
              size="sm"
              variant={tab === t.key ? "default" : "ghost"}
              aria-current={tab === t.key ? "page" : undefined}
              onClick={() => setTab(t.key)}
            >
              {t.label}
            </Button>
          ))}
        </nav>
        <div className="ml-auto flex items-center gap-1">
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label={theme === "dark" ? "切换到亮色主题" : "切换到暗色主题"}
            onClick={toggle}
          >
            {theme === "dark" ? (
              <Sun className="size-4" aria-hidden />
            ) : (
              <Moon className="size-4" aria-hidden />
            )}
          </Button>
          <Button
            size="sm"
            variant="ghost-destructive"
            onClick={() => {
              setToken("");
              setLogged(false);
            }}
          >
            退出
          </Button>
        </div>
      </header>
      <main className="max-w-6xl mx-auto p-4 md:p-6">
        {tab === "overview" && <Overview />}
        {tab === "keys" && <Keys />}
        {tab === "usage" && <Usage />}
        {tab === "providers" && <Providers />}
        {tab === "pricing" && <Pricing />}
      </main>
    </div>
  );
}
