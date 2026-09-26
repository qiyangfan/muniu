// 供应商页:已接入拓扑健康(只读)+ 官方端点预设目录(复制 YAML 片段填入 muniu.yaml 即可接入)。
import { useEffect, useState } from "react";
import type { VariantProps } from "class-variance-authority";
import { Check, Copy } from "lucide-react";
import { api, type ProviderInfo, type ProviderPreset } from "../lib/api";
import { Badge, badgeVariants } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader } from "../components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../components/ui/select";
import { ErrorAlert } from "../components/ui/alert";
import { DataList, type DataCol } from "../components/data-list";
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

// 适配器 → 展示名;与 config 白名单/adapter.For 已实现集合同轴(azure/ollama 并非可配置适配器)
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

// 预设分组 → 中文;键与后端 openapi enum(domestic|global|local)同轴
const GROUP_LABELS: Record<string, string> = {
  domestic: "国内官方",
  global: "国际官方",
  local: "本地运行时",
};

// 预设 → 可粘贴的 providers 段 YAML 片段(与 config.Provider 字段同轴)
const presetYAML = (p: ProviderPreset): string =>
  [
    `  - name: ${p.alias}`,
    `    adapter: ${p.adapter}`,
    `    base_url: "${p.base_url}"`,
    // 鉴权形态声明:国内 Anthropic 分支按其官方文档口径声明 Bearer;官方 Anthropic 缺省 x-api-key
    ...(p.auth ? [`    auth: ${p.auth}`] : []),
    `    keys:`,
    `      - env: ${p.env}   # 密钥明文只放环境变量`,
  ].join("\n");

// 剪贴板写入:clipboard API 仅安全上下文可用,经 LAN IP 访问控制台时回退 execCommand
async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try {
      ok = document.execCommand("copy");
    } catch {
      ok = false;
    }
    ta.remove();
    return ok;
  }
}

// ---------- 官方端点预设目录 ----------

function PresetCatalog() {
  const [presets, setPresets] = useState<ProviderPreset[]>([]);
  const [err, setErr] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [group, setGroup] = useState("all"); // 分组筛选(all|domestic|global|local)
  const [copied, setCopied] = useState<string | null>(null); // 刚复制成功的 alias(按钮反馈)
  const [copyErr, setCopyErr] = useState("");

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

  const copy = async (p: ProviderPreset) => {
    setCopyErr("");
    const alias = p.alias ?? "";
    if (await copyText(presetYAML(p))) {
      setCopied(alias);
      // 短暂反馈后复原;闭包比对防止乱序定时器误清后一次复制
      window.setTimeout(() => setCopied((cur) => (cur === alias ? null : cur)), 2000);
    } else {
      setCopyErr("复制失败,请手动复制表格中的端点信息");
    }
  };

  // 列定义单源:桌面表格与移动预设卡片共用;厂商列同格承载 display+alias
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
        <Button
          size={mobile ? "sm" : "xs"}
          variant="outline"
          onClick={() => copy(p)}
          aria-label={`复制 ${p.display} 的 YAML 片段`}
        >
          {copied === p.alias ? (
            <>
              <Check className="size-3.5" aria-hidden /> 已复制
            </>
          ) : (
            <>
              <Copy className="size-3.5" aria-hidden /> 复制 YAML
            </>
          )}
        </Button>
      ),
    },
  ];

  if (err) {
    return <ErrorAlert>预设目录加载失败:{err}</ErrorAlert>;
  }
  if (!loaded) {
    return <Skeleton className="h-48" />;
  }
  const shown = presets.filter((p) => group === "all" || p.group === group);
  return (
    // 卡头走 CardHeader/CardDescription/CardContent 标准件(与 Overview 同构);卡题用原生
    // h2 承载 CardTitle 视觉配方,语义取舍见 h2 处注释
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          {/* 卡题用原生 h2 承载 CardTitle 视觉配方:CardTitle 基线是 div(kongming 同源)无标题
              语义,标题可达性优先;标准件补 heading 语义属 ui/ 语义级改动,须 kongming 先行 */}
          <h2 className="text-base font-semibold">官方端点预设目录</h2>
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
        {copyErr && <ErrorAlert>{copyErr}</ErrorAlert>}
        {/* 列定义单源:桌面表格(min-w 横向滚动)+ 移动端预设卡片堆叠 */}
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
  return (
    // 页面顶层块间距全站一档 space-y-4(与表格/卡片密度档一致,宪法 Article 2)
    <div className="space-y-4">
      <section className="space-y-3">
        {/* 区块标题(非卡题):多卡区域的组标签;字阶与卡题同档,层级语义为
            「区块 ≥ 卡片标题」(宪法 Article 7),故不经 CardTitle 标准件 */}
        <h2 className="text-base font-semibold">已接入供应商</h2>
        {err ? (
          <ErrorAlert>加载失败:{err}</ErrorAlert>
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
                      <Badge variant={STATE_VARIANT[k.state ?? "unknown"]}>
                        {STATE_LABELS[k.state ?? "unknown"]}
                      </Badge>
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
