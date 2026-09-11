// 用量明细:维度筛选 + 时间范围 + 聚合表。
import { useEffect, useState } from "react";
import { api, type UsagePoint } from "../lib/api";

const DIMS = ["day", "key", "model", "provider"] as const;

export default function Usage() {
  const [dim, setDim] = useState<(typeof DIMS)[number]>("day");
  const [points, setPoints] = useState<UsagePoint[]>([]);
  const [err, setErr] = useState("");

  useEffect(() => {
    // 401/网络错误必须可见:静默吞掉会渲染成「空报表」,与真实零用量无法区分
    api.usage({ group_by: dim })
      .then((r) => { setPoints(r.points); setErr(""); })
      .catch((e) => setErr(e.message));
  }, [dim]);

  return (
    <div className="space-y-4">
      {err && <div className="bg-red-50 border border-red-200 rounded-md p-3 text-sm text-red-700">加载失败:{err}</div>}
      <div className="flex gap-2">
        {DIMS.map((d) => (
          <button
            key={d}
            onClick={() => setDim(d)}
            className={"px-3 py-1.5 rounded-md text-sm " + (dim === d ? "bg-[var(--color-brand)] text-white" : "bg-[var(--color-panel)] text-[var(--color-muted)]")}
          >
            {d}
          </button>
        ))}
      </div>
      <table className="w-full bg-[var(--color-panel)] rounded-xl text-sm">
        <thead>
          <tr className="text-left text-[var(--color-muted)]">
            <th className="p-3">维度</th><th className="p-3">请求</th><th className="p-3">输入 tok</th>
            <th className="p-3">输出 tok</th><th className="p-3">缓存命中</th><th className="p-3">成本 μ</th>
          </tr>
        </thead>
        <tbody>
          {points.map((p) => (
            <tr key={p.dim} className="border-t border-black/5">
              <td className="p-3 font-mono">{p.dim}</td>
              <td className="p-3">{p.requests}</td>
              <td className="p-3">{p.prompt_tokens}</td>
              <td className="p-3">{p.completion_tokens}</td>
              <td className="p-3">{p.cache_hits}</td>
              <td className="p-3">{p.cost_micro}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
