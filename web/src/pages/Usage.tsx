// 用量明细:维度筛选 + 时间范围 + 聚合表。
import { useEffect, useState } from "react";
import { api, type UsagePoint } from "../lib/api";
import { Button } from "../components/ui/button";
import { ErrorAlert } from "../components/ui/alert";
import { Table, TBody, TD, TH, THead, TR } from "../components/ui/table";
import { Skeleton } from "../components/ui/skeleton";
import { EmptyState } from "../components/empty-state";
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

  return (
    <div className="space-y-4">
      {err && <ErrorAlert>加载失败:{err}</ErrorAlert>}
      <div className="flex flex-wrap items-center gap-2">
        {DIMS.map((d) => (
          <Button
            key={d}
            size="sm"
            variant={dim === d ? "default" : "secondary"}
            aria-pressed={dim === d}
            onClick={() => setDim(d)}
          >
            {DIM_LABELS[d]}
          </Button>
        ))}
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <span className="text-xs text-muted-foreground">时间范围</span>
          {RANGES.map((r) => (
            <Button
              key={r}
              size="sm"
              variant={days === r ? "default" : "secondary"}
              aria-pressed={days === r}
              onClick={() => setDays(r)}
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
      {!loaded ? (
        <div className="space-y-2">
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
        </div>
      ) : (
        <Table>
          <THead>
            <TR>
              <TH>{DIM_COL[dim]}</TH>
              <TH>请求</TH>
              <TH>输入 Tokens</TH>
              <TH>输出 Tokens</TH>
              <TH>缓存命中</TH>
              <TH title="1 credit = 十亿分之一本币">成本(credit)</TH>
            </TR>
          </THead>
          <TBody>
            {points.length === 0 ? (
              <TR>
                <TD colSpan={6}>
                  <EmptyState>暂无用量数据</EmptyState>
                </TD>
              </TR>
            ) : (
              points.map((p) => (
                <TR key={p.dim}>
                  <TD className="font-mono" title={dim === "key" ? p.dim : undefined}>
                    {dimText(p.dim)}
                  </TD>
                  <TD>{formatNumber(p.requests)}</TD>
                  <TD>{formatNumber(p.prompt_tokens)}</TD>
                  <TD>{formatNumber(p.completion_tokens)}</TD>
                  <TD>{formatNumber(p.cache_hits)}</TD>
                  <TD>{fmtCredits(p.cost_credits ?? 0)}</TD>
                </TR>
              ))
            )}
          </TBody>
        </Table>
      )}
    </div>
  );
}
