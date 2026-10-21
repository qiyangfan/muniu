// 查看已接入供应商状态并复制官方端点配置。
import { useEffect, useState } from "react";
import { api, type ProviderInfo, type ProviderPreset } from "../lib/api";
import { Badge } from "../components/ui/badge";
import { StatusBadge, type StatusMeta } from "../components/status-badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../components/ui/select";
import { ErrorAlert } from "../components/error-alert";
import { CopyButton } from "../components/copy-button";
import { DataList, type DataCol } from "../components/data-list";
import { EmptyState } from "../components/empty-state";
import { Skeleton } from "../components/ui/skeleton";

// key 池状态统一使用 StatusBadge 展示。
const STATE_META: StatusMeta = {
  healthy: { label: "正常", variant: "success" },
  cooldown: { label: "冷却中", variant: "warning" },
  disabled: { label: "已停用", variant: "destructive" },
  unknown: { label: "未知", variant: "secondary" },
};

// 可配置适配器的展示名。
const ADAPTER_LABELS: Record<string, string> = {
  openai: "OpenAI",
  deepseek: "DeepSeek",
  moonshot: "Moonshot",
  glm: "GLM",
  qwen: "Qwen",
  doubao: "Doubao",
  anthropic: "Anthropic",
};

const adapterLabel = (adapter: string | undefined): string => {
  const key = (adapter ?? "").toLowerCase();
  return ADAPTER_LABELS[key] ?? adapter ?? "—";
};

// 预设分组的中文名称。
const GROUP_LABELS: Record<string, string> = {
  domestic: "国内官方",
  global: "国际官方",
  local: "本地运行时",
};

// 将预设转换为可粘贴到 providers 的 YAML 片段。
const presetYAML = (p: ProviderPreset): string =>
  [
    `  - name: ${p.alias}`,
    `    adapter: ${p.adapter}`,
    `    base_url: "${p.base_url}"`,
    // 按预设声明设置鉴权方式。
    ...(p.auth ? [`    auth: ${p.auth}`] : []),
    `    keys:`,
    `      - env: ${p.env}   # 密钥明文只放环境变量`,
  ].join("\n");

// ---------- 官方端点预设目录 ----------

function PresetCatalog() {
  const [presets, setPresets] = useState<ProviderPreset[]>([]);
  const [err, setErr] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [group, setGroup] = useState("all"); // 分组筛选(all|domestic|global|local)

  useEffect(() => {
    api
      .providerPresets()
      .then((r) => {
        setPresets(r.presets);
        setErr("");
      })
      .catch((e) => setErr(e.message))
      .finally(() => setLoaded(true));
  }, []);

  // 同一列定义驱动桌面表格和移动卡片。
  const presetColumns: DataCol<ProviderPreset>[] = [
    {
      key: "vendor",
      label: "厂商",
      primary: true,
      render: (p) => (
        <>
          <div className="font-medium">{p.display}</div>
          <div className="font-mono text-xs font-normal text-muted-foreground">{p.alias}</div>
        </>
      ),
    },
    {
      key: "adapter",
      label: "适配器",
      secondary: true,
      render: (p) => <Badge variant="secondary">{adapterLabel(p.adapter)}</Badge>,
    },
    {
      key: "base_url",
      label: "base_url",
      full: true,
      cellClassName: "w-64 min-w-60 font-mono text-xs break-all whitespace-normal",
      valueClassName: "font-mono text-xs break-all",
      render: (p) => p.base_url,
    },
    {
      key: "env",
      label: "key 环境变量",
      cellClassName: "font-mono text-xs",
      valueClassName: "font-mono text-xs",
      render: (p) => p.env,
    },
    {
      key: "note",
      label: "说明",
      full: true,
      cellClassName: "w-56 min-w-52 text-xs break-all whitespace-normal text-muted-foreground",
      valueClassName: "text-xs text-muted-foreground",
      render: (p) => (
        <>
          {p.note && <div>{p.note}</div>}
          {(p.models ?? []).length > 0 && <div>典型模型:{(p.models ?? []).join("、")}</div>}
          <a
            className="underline underline-offset-2 hover:text-foreground"
            href={p.docs}
            target="_blank"
            rel="noreferrer"
          >
            官方文档 ↗
          </a>
        </>
      ),
    },
    {
      key: "actions",
      label: <span className="sr-only">操作</span>,
      actions: true,
      render: (p, mobile) => (
        <CopyButton
          value={presetYAML(p)}
          label="复制 YAML"
          size={mobile ? "sm" : "xs"}
          variant="outline"
          failHint="复制失败,请手动复制表格中的端点信息"
          aria-label={`复制 ${p.display} 的 YAML 片段`}
        />
      ),
    },
  ];

  if (err) {
    return <ErrorAlert text={`预设目录加载失败:${err}`} />;
  }
  if (!loaded) {
    return <Skeleton className="h-48" />;
  }
  const shown = presets.filter((p) => group === "all" || p.group === group);
  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          {/* 该卡片标题是本页二级标题。 */}
          <CardTitle as="h2">官方端点预设目录</CardTitle>
          <div className="ml-auto w-full sm:w-40">
            <Select value={group} onValueChange={setGroup}>
              <SelectTrigger aria-label="按分组筛选预设" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部分组</SelectItem>
                {Object.entries(GROUP_LABELS).map(([k, v]) => (
                  <SelectItem key={k} value={k}>
                    {v}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>
        <CardDescription>
          一手官方厂商接入预设(不含第三方中转站)。复制 YAML 片段填入 muniu.yaml 的 providers
          段,配好环境变量后重启即接入;模型名以官方文档为准。Anthropic
          分支端点经内置桥接接入,工具多轮中途暂不支持。
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {/* 响应式供应商列表。 */}
        <DataList
          tableClassName="min-w-[1000px]"
          columns={presetColumns}
          rows={shown}
          rowKey={(p) => p.alias ?? ""}
          empty="该分组暂无预设"
        />
      </CardContent>
    </Card>
  );
}

// ---------- 已接入拓扑健康 ----------

export default function Providers() {
  const [providers, setProviders] = useState<ProviderInfo[]>([]);
  const [err, setErr] = useState("");
  // 区分加载中与空数据，避免请求完成前显示空状态。
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
  return (
    <div className="space-y-4">
      <section className="space-y-3">
        {/* 供应商状态区块标题。 */}
        <CardTitle as="h2">已接入供应商</CardTitle>
        {err ? (
          <ErrorAlert text={`加载失败:${err}`} />
        ) : !loaded ? (
          <div className="grid gap-4 md:grid-cols-2">
            <Skeleton className="h-28" />
            <Skeleton className="h-28" />
          </div>
        ) : providers.length === 0 ? (
          <EmptyState>暂无供应商</EmptyState>
        ) : (
          <div className="grid gap-4 md:grid-cols-2">
            {providers.map((p) => (
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
                      <StatusBadge
                        meta={STATE_META}
                        value={k.state ?? "unknown"}
                        fallbackVariant="secondary"
                      />
                    </span>
                  ))}
                </div>
              </Card>
            ))}
          </div>
        )}
      </section>
      <PresetCatalog />
    </div>
  );
}
