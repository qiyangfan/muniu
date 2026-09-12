// 用量明细:维度筛选 + 时间范围 + 聚合表。
import { useEffect, useState } from "react";
import { api, type UsagePoint } from "../lib/api";
import { Button } from "../components/ui/button";
import { ErrorAlert } from "../components/ui/alert";
import { Table, TBody, TD, TH, THead, TR } from "../components/ui/table";
import { Skeleton } from "../components/ui/skeleton";
import { EmptyState } from "../components/empty-state";

const DIMS = ["day", "key", "model", "provider"] as const;

export default function Usage() {
  const [dim, setDim] = useState<(typeof DIMS)[number]>("day");
  const [points, setPoints] = useState<UsagePoint[]>([]);
  const [err, setErr] = useState("");
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    // 401/网络错误必须可见:静默吞掉会渲染成「空报表」,与真实零用量无法区分
    api
      .usage({ group_by: dim })
      .then((r) => {
        setPoints(r.points);
        setErr("");
        setLoaded(true);
      })
      .catch((e) => {
        setErr(e.message);
        setLoaded(true);
      });
  }, [dim]);

  return (
    <div className="space-y-4">
      {err && <ErrorAlert>加载失败:{err}</ErrorAlert>}
      <div className="flex gap-2">
        {DIMS.map((d) => (
          <Button
            key={d}
            size="sm"
            variant={dim === d ? "default" : "secondary"}
            aria-pressed={dim === d}
            onClick={() => setDim(d)}
          >
            {d}
          </Button>
        ))}
      </div>
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
              <TH>维度</TH>
              <TH>请求</TH>
              <TH>输入 tok</TH>
              <TH>输出 tok</TH>
              <TH>缓存命中</TH>
              <TH>成本 μ</TH>
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
                  <TD className="font-mono">{p.dim}</TD>
                  <TD>{p.requests}</TD>
                  <TD>{p.prompt_tokens}</TD>
                  <TD>{p.completion_tokens}</TD>
                  <TD>{p.cache_hits}</TD>
                  <TD>{p.cost_micro}</TD>
                </TR>
              ))
            )}
          </TBody>
        </Table>
      )}
    </div>
  );
}
