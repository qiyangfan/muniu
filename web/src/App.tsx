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
  UserRoundCheck,
  Wallet,
  type LucideIcon,
} from "lucide-react";
import { api, ApiError, setToken, type AdminActor } from "./lib/api";
import { useTheme } from "./lib/theme";
import { cn, errorMessage } from "./lib/utils";
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
import OAuthAccounts from "./pages/OAuthAccounts";
import Audit from "./pages/Audit";

// readOidcError 读取联邦回调失败文案(首渲染即用,避免 effect 内 setState)。
function readOidcError(): string {
  return new URLSearchParams(window.location.search).get("oidc_error") ?? "";
}

// stripOidcError 展示后清除查询参数,避免刷新重复提示。
function stripOidcError(): void {
  const params = new URLSearchParams(window.location.search);
  if (!params.has("oidc_error")) return;
  params.delete("oidc_error");
  const qs = params.toString();
  window.history.replaceState(null, "", `${window.location.pathname}${qs ? `?${qs}` : ""}`);
}

// 三种登录轨道均以 whoami 恢复会话；会话凭据由 HttpOnly cookie 保存，
// 锦官联邦整页跳转经 OIDC 授权码 + PKCE，失败回带 oidc_error 查询参数。
function Login({ onOk }: { onOk: (actor: AdminActor) => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [tokenMode, setTokenMode] = useState(false);
  const [token, setLoginToken] = useState("");
  const [err, setErr] = useState(readOidcError);
  const [busy, setBusy] = useState(false);
  const [federation, setFederation] = useState(false);
  useEffect(() => {
    api
      .federationInfo()
      .then((info) => setFederation(info.enabled))
      .catch(() => setFederation(false));
    stripOidcError();
  }, []);
  const submitLogin = async () => {
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
      setErr(errorMessage(e2));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="min-h-dvh grid place-items-center bg-background px-4">
      <Card className="w-full max-w-96">
        <CardHeader>
          <CardTitle as="h2">木牛控制台</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {federation && (
            <>
              <Button
                type="button"
                variant="outline"
                className="w-full"
                onClick={() => {
                  // 清掉残留的共享令牌:否则联邦会话过期后会静默回落到旧令牌身份
                  setToken("");
                  window.location.href = "/admin/v1/auth/oidc/start";
                }}
              >
                锦官登录
              </Button>
              <div className="flex items-center gap-3 text-xs text-muted-foreground" aria-hidden>
                <span className="h-px flex-1 bg-border" />
                或使用本地凭据
                <span className="h-px flex-1 bg-border" />
              </div>
            </>
          )}
          <form
            className="grid gap-4"
            onSubmit={(e) => {
              e.preventDefault();
              void submitLogin();
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
  { key: "oauth", label: "订阅账号", shortLabel: "订阅", icon: UserRoundCheck },
  { key: "balances", label: "余额告警", shortLabel: "余额", icon: Wallet },
  { key: "pricing", label: "价目管理", shortLabel: "价目", icon: Tags },
  { key: "audit", label: "操作审计", shortLabel: "审计", icon: ClipboardList },
];

type TabKey =
  "overview" | "keys" | "usage" | "providers" | "oauth" | "balances" | "pricing" | "audit";

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
        if (live && (!(e instanceof ApiError) || e.status !== 401)) setProbeError(errorMessage(e));
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
  const doLogout = async () => {
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
        setProbeError(errorMessage(e));
      }
    }
  };
  const logout = (
    <Button
      size="sm"
      variant="ghost-destructive"
      onClick={() => {
        void doLogout();
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
        {tab === "oauth" && <OAuthAccounts />}
        {tab === "balances" && <Balances />}
        {tab === "pricing" && <Pricing />}
        {tab === "audit" && <Audit />}
      </main>
      {/* 移动端使用底部导航，并为 iOS 安全区留出空间。 */}
      <nav
        className="fixed inset-x-0 bottom-0 z-40 border-t border-border bg-card md:hidden"
        aria-label="主导航"
      >
        {/* 网格列数需与 TABS.length 一致(8 页签靠窄内距+小字阶在 375 档单排放下)。 */}
        <div className="grid grid-cols-8 pb-[env(safe-area-inset-bottom)]">
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
