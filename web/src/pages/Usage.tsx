// 用量明细:维度筛选 + 时间范围 + 聚合表。
import { useEffect, useState } from "react";
import { api, type UsagePoint } from "../lib/api";
import { Button } from "../components/ui/button";
import { ErrorAlert } from "../components/ui/alert";
import { DataList, type DataCol } from "../components/data-list";
import { formatDateTime, formatNumber } from "../lib/datetime";
import { fmtCredits } from "../lib/utils";

const DIMS = ["day", "key", "model", "provider"] as const;
type Dim = (typeof DIMS)[number];

// 英文参数值是后端契约,仅显示层做中文映射
const DIM_LABELS: Record<Dim, string> = {
  day: "按日",
  key: "按密钥",
  model: "按模型",
  provider: "按供应商",
};
// 首列列头随维度变化,比统一「维度」更可读
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
  const [points, setPoints] = useState<UsagePoint[]>([]);
  const [keyNames, setKeyNames] = useState<Map<string, string>>(new Map());
  const [range, setRange] = useState<{ from: Date; to: Date } | null>(null);
  const [err, setErr] = useState("");
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    // 401/网络错误必须可见:静默吞掉会渲染成「空报表」,与真实零用量无法区分。
    // 快速切换维度/范围时丢弃过期响应(cleanup 置 cancelled),防旧数据配新表头
    let cancelled = false;
    const to = new Date();
    const from = new Date(to.getTime() - days * 24 * 60 * 60 * 1000);
    setRange({ from, to });
    setLoaded(false);
    api
      .usage({ group_by: dim, from: from.toISOString(), to: to.toISOString() })
      .then((r) => {
        if (cancelled) return;
        setPoints(r.points);
        setErr("");
        setLoaded(true);
      })
      .catch((e) => {
        if (cancelled) return;
        setErr(e.message);
        setLoaded(true);
      });
    // 按密钥维度时 dim 是内部 api_key_id,并行拉密钥清单建 id→前缀映射给人话显示;
    // 映射失败回退显示原始 ID,不阻塞报表
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
    } else {
      setKeyNames(new Map());
    }
    return () => {
      cancelled = true;
    };
  }, [dim, days]);

  // 密钥维度值 → 「前缀…」;已吊销等映射缺失时回退完整 ID(单元格 title 亦标完整值)
  const dimText = (v: string | undefined) => {
    if (dim !== "key" || !v) return v ?? "—";
    const prefix = keyNames.get(v);
    return prefix ? `${prefix}…` : v;
  };

  // 列定义单源:桌面表格与移动卡片共用
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
    <div className="space-y-4">
      {err && <ErrorAlert>加载失败:{err}</ErrorAlert>}
      {/* 筛选分组:移动端两行各占一档、按钮等宽(分段控件手感),sm+ 恢复同行左组+右组 */}
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
    </div>
  );
}
