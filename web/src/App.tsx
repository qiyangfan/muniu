import { useEffect, useState } from "react";
import {
  ChartColumn,
  KeyRound,
  LayoutDashboard,
  Moon,
  Server,
  Sun,
  Tags,
  type LucideIcon,
} from "lucide-react";
import { api, ApiError, getToken, setToken } from "./lib/api";
import { useTheme } from "./lib/theme";
import { cn } from "./lib/utils";
import { Button } from "./components/ui/button";
import { Input } from "./components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "./components/ui/card";
import { ErrorAlert } from "./components/error-alert";
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
    <div className="min-h-dvh grid place-items-center bg-background px-4">
      <Card className="w-full max-w-96">
        <CardHeader>
          {/* 登录卡题即页内最高级,升页面级 h2(CardTitle 标题语义,kongming 对齐);
              原 text-xl 密档补偿随视觉收敛废止 */}
          <CardTitle as="h2">木牛控制台</CardTitle>
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
            {err && <ErrorAlert text={err} />}
            <Button type="submit" className="w-full" disabled={busy}>
              {busy ? "校验中…" : "登录"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}

// label=桌面导航完整文案;shortLabel=移动端底部标签(375px 五等分格内不折行)
const TABS: { key: TabKey; label: string; shortLabel: string; icon: LucideIcon }[] = [
  { key: "overview", label: "总览", shortLabel: "总览", icon: LayoutDashboard },
  { key: "keys", label: "密钥管理", shortLabel: "密钥", icon: KeyRound },
  { key: "usage", label: "用量明细", shortLabel: "用量", icon: ChartColumn },
  { key: "providers", label: "供应商", shortLabel: "供应商", icon: Server },
  { key: "pricing", label: "价目管理", shortLabel: "价目", icon: Tags },
];

type TabKey = "overview" | "keys" | "usage" | "providers" | "pricing";

export default function App() {
  const [logged, setLogged] = useState(getToken() !== "");
  const [tab, setTab] = useState<TabKey>("overview");
  const { theme, toggle } = useTheme();
  // 页签切换回顶:滚动残留会让新页面一进来就在中部(移动端长列表下尤其明显)
  useEffect(() => {
    window.scrollTo(0, 0);
  }, [tab]);
  if (!logged) return <Login onOk={() => setLogged(true)} />;

  const themeToggle = (
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
  );
  const logout = (
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
  );

  return (
    <div className="min-h-dvh">
      {/* 顶栏全端粘性;移动端只留品牌+主操作,导航下沉到底部标签栏 */}
      <header className="sticky top-0 z-40 flex h-14 items-center gap-6 border-b border-border bg-card px-4 md:px-6">
        <span className="shrink-0 font-semibold text-primary">木牛 muniu</span>
        <nav className="hidden items-center gap-4 md:flex" aria-label="主导航">
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
          {themeToggle}
          {logout}
        </div>
      </header>
      <main className="mx-auto w-full max-w-6xl p-4 pb-24 md:p-6 md:pb-6">
        {tab === "overview" && <Overview />}
        {tab === "keys" && <Keys />}
        {tab === "usage" && <Usage />}
        {tab === "providers" && <Providers />}
        {tab === "pricing" && <Pricing />}
      </main>
      {/* 移动端主导航:拇指区底部标签栏(图标+短标签),让出 iOS 安全区;md+ 由顶栏接管 */}
      <nav
        className="fixed inset-x-0 bottom-0 z-40 border-t border-border bg-card md:hidden"
        aria-label="主导航"
      >
        {/* grid-cols-5 与 TABS.length 硬绑定:增删页签须同步改列数 */}
        <div className="grid grid-cols-5 pb-[env(safe-area-inset-bottom)]">
          {TABS.map((t) => {
            const active = tab === t.key;
            return (
              <Button
                key={t.key}
                variant="ghost"
                aria-current={active ? "page" : undefined}
                onClick={() => setTab(t.key)}
                className={cn(
                  "h-auto flex-col gap-1 rounded-none px-1 py-2 text-xs",
                  active ? "text-primary" : "text-muted-foreground",
                )}
              >
                <t.icon className="size-5" aria-hidden />
                <span className={cn(active && "font-medium")}>{t.shortLabel}</span>
              </Button>
            );
          })}
        </div>
      </nav>
    </div>
  );
}
