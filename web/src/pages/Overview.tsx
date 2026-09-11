// 总览:按日用量/成本图表 + 语义缓存统计(首屏图表,semantic-cache.md「计量与可观测」)。
import { useEffect, useState } from "react";
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { api, type CacheStats, type UsagePoint } from "../lib/api";

export default function Overview() {
  const [points, setPoints] = useState<UsagePoint[]>([]);
  const [stats, setStats] = useState<CacheStats | null>(null);
  const [err, setErr] = useState("");

  useEffect(() => {
    api.usage({ group_by: "day" }).then((r) => setPoints(r.points)).catch((e) => setErr(String(e)));
    api.cacheStats().then(setStats).catch(() => setStats(null));
  }, []);

  if (err) return <p className="text-[var(--color-danger)]">{err}</p>;
  return (
    <div className="space-y-6">
      <div className="grid grid-cols-3 gap-4">
        <Card label="语义缓存条目" value={stats?.enabled ? String(stats.entries) : "未启用"} />
        <Card label="缓存累计命中" value={stats?.enabled ? String(stats.hits) : "—"} />
        <Card label="缓存阈值" value={stats?.enabled ? String(stats.threshold) : "—"} />
      </div>
      <div className="bg-[var(--color-panel)] rounded-xl p-4 h-80">
        <h2 className="text-sm text-[var(--color-muted)] mb-2">按日用量(请求 / 成本 μ)</h2>
        <ResponsiveContainer width="100%" height="90%">
          <BarChart data={points}>
            <CartesianGrid strokeDasharray="3 3" />
            <XAxis dataKey="dim" />
            <YAxis />
            <Tooltip />
            <Bar dataKey="requests" name="请求数" fill="var(--color-brand)" />
            <Bar dataKey="cost_micro" name="成本 μ" fill="#a8c5b5" />
          </BarChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}

function Card({ label, value }: { label: string; value: string }) {
  return (
    <div className="bg-[var(--color-panel)] rounded-xl p-4">
      <div className="text-xs text-[var(--color-muted)]">{label}</div>
      <div className="text-2xl font-semibold mt-1">{value}</div>
    </div>
  );
}
