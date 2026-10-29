import { useEffect, useState } from "react";
import {
  ClipboardList,
  ChartColumn,
  KeyRound,
  LayoutDashboard,
  Moon,
  Server,
  Sun,
  Tags,
  Wallet,
  type LucideIcon,
} from "lucide-react";
import { api, ApiError, setToken, type AdminActor } from "./lib/api";
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
import Balances from "./pages/Balances";
import Audit from "./pages/Audit";

// 两种登录方式均通过 whoami 恢复会话；会话凭据由 HttpOnly cookie 保存。
function Login({ onOk }: { onOk: (actor: AdminActor) => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [tokenMode, setTokenMode] = useState(false);
  const [token, setLoginToken] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <div className="min-h-dvh grid place-items-center bg-background px-4">
      <Card className="w-full max-w-96">
        <CardHeader>
          <CardTitle as="h2">木牛控制台</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <form
            className="grid gap-4"
            onSubmit={async (e) => {
              e.preventDefault();
              setBusy(true);
              setErr("");
              try {
                setToken("");
                if (tokenMode) {
                  setToken(token.trim());
                  onOk(await api.whoami());
                } else {
                  onOk(await api.login(username, password));
                }
                setPassword("");
              } catch (e2) {
                setToken("");
                setErr(e2 instanceof Error ? e2.message : "登录失败，请稍后重试");
              } finally {
                setBusy(false);
              }
            }}
          >
            {tokenMode ? (
              <FormRow label="管理员令牌" htmlFor="admin-token">
                <Input
                  id="admin-token"
                  type="password"
                  autoComplete="off"
                  value={token}
                  onChange={(e) => setLoginToken(e.target.value)}
                  required
                />
              </FormRow>
            ) : (
              <>
                <FormRow label="用户名" htmlFor="admin-username">
                  <Input
                    id="admin-username"
                    autoComplete="username"
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                    required
                  />
                </FormRow>
                <FormRow label="密码" htmlFor="admin-password">
                  <Input
                    id="admin-password"
                    type="password"
                    autoComplete="current-password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    required
                  />
                </FormRow>
              </>
            )}
            {err && <ErrorAlert text={err} />}
            <Button type="submit" className="w-full" disabled={busy}>
              {busy ? "登录中…" : "登录"}
            </Button>
          </form>
          <Button
            variant="ghost"
            className="w-full"
            onClick={() => {
              setTokenMode((v) => !v);
              setErr("");
            }}
          >
            {tokenMode ? "使用账号登录" : "使用管理令牌登录"}
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}

// label 用于桌面导航，shortLabel 用于移动端导航。
const TABS: { key: TabKey; label: string; shortLabel: string; icon: LucideIcon }[] = [
  { key: "overview", label: "总览", shortLabel: "总览", icon: LayoutDashboard },
  { key: "keys", label: "密钥管理", shortLabel: "密钥", icon: KeyRound },
  { key: "usage", label: "用量明细", shortLabel: "用量", icon: ChartColumn },
  { key: "providers", label: "供应商", shortLabel: "供应商", icon: Server },
  { key: "balances", label: "余额告警", shortLabel: "余额", icon: Wallet },
  { key: "pricing", label: "价目管理", shortLabel: "价目", icon: Tags },
  { key: "audit", label: "操作审计", shortLabel: "审计", icon: ClipboardList },
];

type TabKey = "overview" | "keys" | "usage" | "providers" | "balances" | "pricing" | "audit";

export default function App() {
  const [actor, setActor] = useState<AdminActor | null>(null);
  const [checking, setChecking] = useState(true);
  const [probeError, setProbeError] = useState("");
  useEffect(() => {
    let live = true;
    const expired = () => {
      setToken("");
      setActor(null);
    };
    window.addEventListener("muniu-session-expired", expired);
    api
      .whoami()
      .then((a) => {
        if (live) setActor(a);
      })
      .catch((e) => {
        if (live && (!(e instanceof ApiError) || e.status !== 401)) setProbeError(e.message);
      })
      .finally(() => {
        if (live) setChecking(false);
      });
    return () => {
      live = false;
      window.removeEventListener("muniu-session-expired", expired);
    };
  }, []);
  const [tab, setTab] = useState<TabKey>("overview");
  const { theme, toggle } = useTheme();
  // 切换页签时回到页面顶部。
  useEffect(() => {
    window.scrollTo(0, 0);
  }, [tab]);
  if (checking)
    return (
      <div className="p-6 text-sm text-muted-foreground" role="status">
        正在确认登录状态…
      </div>
    );
  if (probeError)
    return (
      <div className="p-6 space-y-4">
        <ErrorAlert text={probeError} />
        <Button onClick={() => window.location.reload()}>重试</Button>
      </div>
    );
  if (!actor) return <Login onOk={setActor} />;

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
      onClick={async () => {
        try {
          await api.logout();
          setToken("");
          setActor(null);
          setProbeError("");
        } catch (e) {
          if (e instanceof ApiError && e.status === 401) {
            setToken("");
            setActor(null);
            setProbeError("");
          } else {
            setProbeError((e as Error).message);
          }
        }
      }}
    >
      退出
    </Button>
  );

  return (
    <div className="min-h-dvh">
      {/* 移动端顶栏仅保留品牌和主操作；导航移至底部。 */}
      <header className="sticky top-0 z-40 flex h-14 items-center gap-4 border-b border-border bg-card px-4 md:px-6">
        <span className="shrink-0 font-semibold text-primary">木牛 muniu</span>
        <nav className="hidden items-center gap-1 md:flex" aria-label="主导航">
          {TABS.map((t) => (
            <Button
              key={t.key}
              size="sm"
              variant={tab === t.key ? "default" : "ghost"}
              aria-current={tab === t.key ? "page" : undefined}
              onClick={() => setTab(t.key)}
            >
              <span className="hidden lg:inline">{t.label}</span>
              <span className="lg:hidden">{t.shortLabel}</span>
            </Button>
          ))}
        </nav>
        <div className="ml-auto flex items-center gap-1">
          <span className="max-w-24 truncate text-xs text-muted-foreground">
            {actor.kind === "token" ? "共享管理令牌" : actor.name}
          </span>
          {themeToggle}
          {logout}
        </div>
      </header>
      <main className="mx-auto w-full max-w-6xl p-4 pb-24 md:p-6 md:pb-6">
        {tab === "overview" && <Overview />}
        {tab === "keys" && <Keys />}
        {tab === "usage" && <Usage />}
        {tab === "providers" && <Providers />}
        {tab === "balances" && <Balances />}
        {tab === "pricing" && <Pricing />}
        {tab === "audit" && <Audit />}
      </main>
      {/* 移动端使用底部导航，并为 iOS 安全区留出空间。 */}
      <nav
        className="fixed inset-x-0 bottom-0 z-40 border-t border-border bg-card md:hidden"
        aria-label="主导航"
      >
        {/* 网格列数需与 TABS.length 一致。 */}
        <div className="grid grid-cols-7 pb-[env(safe-area-inset-bottom)]">
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
