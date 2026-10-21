// 用量明细按维度和时间范围聚合。
import { useEffect, useState } from "react";
import { api, type UsagePoint } from "../lib/api";
import { Button } from "../components/ui/button";
import { ErrorAlert } from "../components/error-alert";
import { DataList, type DataCol } from "../components/data-list";
import { formatDateTime, formatNumber } from "../lib/datetime";
import { fmtCredits } from "../lib/utils";

const DIMS = ["day", "key", "model", "provider"] as const;
type Dim = (typeof DIMS)[number];

// API 使用英文维度值；页面只翻译显示文案。
const DIM_LABELS: Record<Dim, string> = {
  day: "按日",
  key: "按密钥",
  model: "按模型",
  provider: "按供应商",
};
// 首列标题随统计维度变化。
const DIM_COL: Record<Dim, string> = {
  day: "日期",
  key: "密钥",
  model: "模型",
  provider: "供应商",
};

const RANGES = [7, 30, 90] as const;
type RangeDays = (typeof RANGES)[number];

export default function Usage() {
  const [dim, setDim] = useState<Dim>("day");
  const [days, setDays] = useState<RangeDays>(7);

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
        <div className="flex gap-1.5" role="group" aria-label="统计维度">
          {DIMS.map((d) => (
            <Button
              key={d}
              size="sm"
              variant={dim === d ? "default" : "secondary"}
              aria-pressed={dim === d}
              onClick={() => setDim(d)}
              className="flex-1 sm:flex-none"
            >
              {DIM_LABELS[d]}
            </Button>
          ))}
        </div>
        <div className="flex items-center gap-1.5 sm:ml-auto" role="group" aria-label="时间范围">
          <span className="sr-only sm:not-sr-only sm:text-xs sm:text-muted-foreground">
            时间范围
          </span>
          {RANGES.map((r) => (
            <Button
              key={r}
              size="sm"
              variant={days === r ? "default" : "secondary"}
              aria-pressed={days === r}
              onClick={() => setDays(r)}
              className="flex-1 sm:flex-none"
            >
              近 {r} 天
            </Button>
          ))}
        </div>
      </div>
      {/* 筛选变化时重挂载报表，重置加载态和数据。 */}
      <UsageReport key={`${dim}-${days}`} dim={dim} days={days} />
    </div>
  );
}

function UsageReport({ dim, days }: { dim: Dim; days: RangeDays }) {
  const [points, setPoints] = useState<UsagePoint[]>([]);
  const [keyNames, setKeyNames] = useState<Map<string, string>>(new Map());
  const [range, setRange] = useState<{ from: Date; to: Date } | null>(null);
  const [err, setErr] = useState("");
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    // 显示查询错误，避免与零用量混淆；丢弃卸载后返回的旧请求。
    let cancelled = false;
    const to = new Date();
    const from = new Date(to.getTime() - days * 24 * 60 * 60 * 1000);
    api
      .usage({ group_by: dim, from: from.toISOString(), to: to.toISOString() })
      .then((r) => {
        if (cancelled) return;
        setRange({ from, to });
        setPoints(r.points);
        setErr("");
        setLoaded(true);
      })
      .catch((e) => {
        if (cancelled) return;
        setRange({ from, to });
        setErr(e.message);
        setLoaded(true);
      });
    // 密钥维度使用 ID 查询；补充拉取前缀用于显示，失败时回退到 ID。
    if (dim === "key") {
      api
        .listKeys()
        .then((r) => {
          if (cancelled) return;
          const m = new Map<string, string>();
          for (const k of r.keys) if (k.id && k.prefix) m.set(k.id, k.prefix);
          setKeyNames(m);
        })
        .catch(() => {});
    }
    return () => {
      cancelled = true;
    };
  }, [dim, days]);

  // 密钥维度显示对应前缀；找不到时回退到完整 ID。
  const dimText = (v: string | undefined) => {
    if (dim !== "key" || !v) return v ?? "—";
    const prefix = keyNames.get(v);
    return prefix ? `${prefix}…` : v;
  };

  const columns: DataCol<UsagePoint>[] = [
    {
      key: "dim",
      label: DIM_COL[dim],
      primary: true,
      render: (p) => (
        <span className="font-mono" title={dim === "key" ? p.dim : undefined}>
          {dimText(p.dim)}
        </span>
      ),
    },
    { key: "requests", label: "请求", numeric: true, render: (p) => formatNumber(p.requests) },
    {
      key: "prompt",
      label: "输入 Tokens",
      numeric: true,
      render: (p) => formatNumber(p.prompt_tokens),
    },
    {
      key: "completion",
      label: "输出 Tokens",
      numeric: true,
      render: (p) => formatNumber(p.completion_tokens),
    },
    {
      key: "cache",
      label: "缓存命中",
      numeric: true,
      render: (p) => formatNumber(p.cache_hits),
    },
    {
      key: "cost",
      label: "成本(credit)",
      numeric: true,
      headTitle: "1 credit = 十亿分之一本币",
      render: (p) => fmtCredits(p.cost_credits ?? 0),
    },
  ];

  return (
    <>
      {err && <ErrorAlert text={`加载失败:${err}`} />}
      {range && (
        <p className="text-xs text-muted-foreground">
          统计区间:{formatDateTime(range.from)} 至 {formatDateTime(range.to)}
          ;按日统计以 UTC 零点切分(北京时间早 8 点);成本 1 credit = 十亿分之一本币。
        </p>
      )}
      <DataList
        columns={columns}
        rows={points}
        rowKey={(p) => p.dim ?? ""}
        empty="暂无用量数据"
        loading={!loaded}
        skeletonRows={3}
      />
    </>
  );
}
