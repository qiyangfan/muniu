// 供应商健康:只读拓扑 + key 池三态(healthy/cooldown/disabled)。
import { useEffect, useState } from "react";
import type { VariantProps } from "class-variance-authority";
import { api, type ProviderInfo } from "../lib/api";
import { Badge, badgeVariants } from "../components/ui/badge";
import { Card } from "../components/ui/card";
import { ErrorAlert } from "../components/ui/alert";
import { EmptyState } from "../components/empty-state";
import { Skeleton } from "../components/ui/skeleton";

// 字面色表收编为 Badge 语义变体(宪法 Article 3)
const STATE_VARIANT: Record<string, VariantProps<typeof badgeVariants>["variant"]> = {
  healthy: "success",
  cooldown: "warning",
  disabled: "destructive",
  unknown: "secondary",
};

// key 池状态 → 中文;键与 STATE_VARIANT 一一对应
const STATE_LABELS: Record<string, string> = {
  healthy: "正常",
  cooldown: "冷却中",
  disabled: "已停用",
  unknown: "未知",
};

// 常见 adapter → 供应商展示名;未收录的值原样保留
const ADAPTER_LABELS: Record<string, string> = {
  openai: "OpenAI",
  deepseek: "DeepSeek",
  anthropic: "Anthropic",
  gemini: "Gemini",
  azure: "Azure",
  ollama: "Ollama",
};

const adapterLabel = (adapter: string | undefined): string => {
  const key = (adapter ?? "").toLowerCase();
  return ADAPTER_LABELS[key] ?? adapter ?? "—";
};

export default function Providers() {
  const [providers, setProviders] = useState<ProviderInfo[]>([]);
  const [err, setErr] = useState("");
  // 加载中 ≠ 空数据:无 loaded 标志会在请求在途时闪现「暂无供应商」假事实(评审 P2 修复)
  const [loaded, setLoaded] = useState(false);
  useEffect(() => {
    api
      .providers()
      .then((r) => {
        setProviders(r.providers);
        setErr("");
      })
      .catch((e) => setErr(e.message))
      .finally(() => setLoaded(true));
  }, []);
  if (err) {
    return <ErrorAlert>加载失败:{err}</ErrorAlert>;
  }
  if (!loaded) {
    return (
      <div className="grid gap-4 md:grid-cols-2">
        <Skeleton className="h-28" />
        <Skeleton className="h-28" />
      </div>
    );
  }
  return (
    <div className="grid gap-4 md:grid-cols-2">
      {providers.length === 0 ? (
        <EmptyState className="md:col-span-2">暂无供应商</EmptyState>
      ) : (
        providers.map((p) => (
          <Card key={p.name} className="space-y-2 p-4">
            <div className="flex justify-between">
              <span className="font-semibold">{p.name}</span>
              <span className="text-xs text-muted-foreground">{adapterLabel(p.adapter)}</span>
            </div>
            <div className="text-xs break-all text-muted-foreground">{p.base_url}</div>
            <div className="text-xs text-muted-foreground">
              权重 {p.weight} · 优先级 {p.priority}
            </div>
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
              {(p.keys ?? []).map((k) => (
                <span key={k.id} className="flex items-center gap-1.5 text-xs">
                  <span className="font-mono">{k.id}</span>
                  <Badge variant={STATE_VARIANT[k.state ?? "unknown"]}>
                    {STATE_LABELS[k.state ?? "unknown"]}
                  </Badge>
                </span>
              ))}
            </div>
          </Card>
        ))
      )}
    </div>
  );
}
