// 总览展示按日用量、成本和语义缓存统计。
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
  // 加载期间与零数据状态分开显示。
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
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-3">
        <StatCard
          label="语义缓存条目"
          value={stats?.enabled ? formatNumber(stats.entries) : "未启用"}
        />
        <StatCard label="缓存累计命中" value={stats?.enabled ? formatNumber(stats.hits) : "—"} />
        {/* 相似度阈值是 0 到 1 的小数，不按数量格式化。 */}
        <StatCard label="缓存相似度阈值" value={stats?.enabled ? String(stats.threshold) : "—"} />
      </div>
      <Card>
        <CardHeader>
          <CardTitle>按日用量</CardTitle>
          <CardDescription>请求数与成本;成本 1 credit = 十亿分之一本币</CardDescription>
        </CardHeader>
        <CardContent>
          {/* 空数据时图表不显示坐标轴，使用明确的空状态提示。 */}
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
                      // 请求数使用千分位；成本使用 credit 缩写。
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
