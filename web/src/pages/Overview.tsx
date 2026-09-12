// 总览:按日用量/成本图表 + 语义缓存统计(首屏图表,semantic-cache.md「计量与可观测」)。
import { useEffect, useState } from "react";
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { api, type CacheStats, type UsagePoint } from "../lib/api";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { ErrorAlert } from "../components/ui/alert";
import { StatCard } from "../components/stat-card";

export default function Overview() {
  const [points, setPoints] = useState<UsagePoint[]>([]);
  const [stats, setStats] = useState<CacheStats | null>(null);
  const [err, setErr] = useState("");

  useEffect(() => {
    api
      .usage({ group_by: "day" })
      .then((r) => setPoints(r.points))
      .catch((e) => setErr(String(e)));
    api
      .cacheStats()
      .then(setStats)
      .catch(() => setStats(null));
  }, []);

  if (err) return <ErrorAlert>{err}</ErrorAlert>;
  return (
    <div className="space-y-6">
      <div className="grid gap-4 md:grid-cols-3">
        <StatCard label="语义缓存条目" value={stats?.enabled ? String(stats.entries) : "未启用"} />
        <StatCard label="缓存累计命中" value={stats?.enabled ? String(stats.hits) : "—"} />
        <StatCard label="缓存阈值" value={stats?.enabled ? String(stats.threshold) : "—"} />
      </div>
      <Card>
        <CardHeader>
          <CardTitle>按日用量</CardTitle>
          <CardDescription>请求 / 成本 μ</CardDescription>
        </CardHeader>
        <CardContent>
          {/* 图表取色唯一走 token 变量(宪法 Article 3/10,禁字面 hex):
              recharts 浮层/坐标默认值是字面白与 #666,暗色下成刺眼白块,全部显式覆盖 */}
          <div className="h-64">
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
                />
                <Tooltip
                  cursor={{ fill: "var(--color-accent)" }}
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
                <Bar dataKey="cost_micro" name="成本 μ" fill="var(--color-chart-series-2)" />
              </BarChart>
            </ResponsiveContainer>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
