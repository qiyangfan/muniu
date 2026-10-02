// 总览:按日用量/成本图表 + 语义缓存统计(首屏图表,semantic-cache.md「计量与可观测」)。
import { useEffect, useState } from "react";
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { api, type CacheStats, type UsagePoint } from "../lib/api";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { ErrorAlert } from "../components/error-alert";
import { EmptyState } from "../components/empty-state";
import { Skeleton } from "../components/ui/skeleton";
import { StatCard } from "../components/stat-card";
import { formatNumber } from "../lib/datetime";
import { fmtCredits } from "../lib/utils";

export default function Overview() {
  const [points, setPoints] = useState<UsagePoint[]>([]);
  const [stats, setStats] = useState<CacheStats | null>(null);
  const [err, setErr] = useState("");
  // 加载中 ≠ 空数据:无 loaded 标志会在请求在途时闪现「暂无数据」假事实
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    api
      .usage({ group_by: "day" })
      .then((r) => setPoints(r.points))
      .catch((e) => setErr(e instanceof Error ? e.message : "加载失败,请稍后重试"))
      .finally(() => setLoaded(true));
    api
      .cacheStats()
      .then(setStats)
      .catch(() => setStats(null));
  }, []);

  if (err) return <ErrorAlert text={`错误:${err}`} />;
  return (
    // 页面顶层块间距全站一档 space-y-4(与表格/卡片密度档一致,宪法 Article 2)
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-3">
        <StatCard
          label="语义缓存条目"
          value={stats?.enabled ? formatNumber(stats.entries) : "未启用"}
        />
        <StatCard label="缓存累计命中" value={stats?.enabled ? formatNumber(stats.hits) : "—"} />
        {/* 相似度阈值是 0~1 小数(如 0.97),非数量,不走千分位 */}
        <StatCard label="缓存相似度阈值" value={stats?.enabled ? String(stats.threshold) : "—"} />
      </div>
      <Card>
        <CardHeader>
          <CardTitle>按日用量</CardTitle>
          <CardDescription>请求数与成本;成本 1 credit = 十亿分之一本币</CardDescription>
        </CardHeader>
        <CardContent>
          {/* 空数据态:零数据时 recharts 连坐标轴都不渲染,只剩无解释空白——禁静默空白;
              文案只说用户事实,不暴露存储/汇形态等实现细节 */}
          {!loaded ? (
            <Skeleton className="h-48 w-full sm:h-64" />
          ) : points.length === 0 ? (
            <EmptyState>暂无用量数据</EmptyState>
          ) : (
            <div className="h-48 sm:h-64">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={points}>
                  <CartesianGrid strokeDasharray="3 3" stroke="var(--color-border)" />
                  <XAxis
                    dataKey="dim"
                    stroke="var(--color-border)"
                    tick={{ fill: "var(--color-muted-foreground)" }}
                  />
                  <YAxis
                    stroke="var(--color-border)"
                    tick={{ fill: "var(--color-muted-foreground)" }}
                    tickFormatter={(v) => fmtCredits(Number(v))}
                  />
                  <Tooltip
                    cursor={{ fill: "var(--color-accent)" }}
                    formatter={(v, name) =>
                      // 请求数是次数不是 credit,不得套 credit 缩写;成本(credit)走大数缩写;
                      // 千分位走 datetime 单源(锁 zh-CN 分组,禁裸 toLocaleString 漂移 locale)
                      name === "请求数" ? formatNumber(Number(v)) : fmtCredits(Number(v))
                    }
                    contentStyle={{
                      backgroundColor: "var(--color-popover)",
                      border: "1px solid var(--color-border)",
                      borderRadius: 8,
                      color: "var(--color-foreground)",
                    }}
                    labelStyle={{ color: "var(--color-muted-foreground)" }}
                    itemStyle={{ color: "var(--color-foreground)" }}
                  />
                  <Bar dataKey="requests" name="请求数" fill="var(--color-primary)" />
                  <Bar
                    dataKey="cost_credits"
                    name="成本(credit)"
                    fill="var(--color-chart-series-2)"
                  />
                </BarChart>
              </ResponsiveContainer>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
