// 价目管理:在线改价(动态价目,metering.md「动态价目」)——列表/编辑/撤销覆盖。
// 价目为运营态数据:改价即时生效零重启;静态配置价的行经编辑即建 DB 覆盖。
import { useCallback, useEffect, useState } from "react";
import { Plus, X } from "lucide-react";
import { api, type PricingEntry } from "../lib/api";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Select } from "../components/ui/select";
import { Badge } from "../components/ui/badge";
import { Card } from "../components/ui/card";
import { ErrorAlert } from "../components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "../components/ui/alert-dialog";
import { Table, TBody, TD, TH, THead, TR } from "../components/ui/table";
import { EmptyState } from "../components/empty-state";
import { FormRow } from "../components/form-row";
import { formatDateTime } from "../lib/datetime";

const EMPTY: PricingEntry = {
  currency: "CNY",
  input: "",
  cached_input: "",
  output: "",
  timezone: "UTC",
  time_windows: [],
};

// 时区选项:后端仅接受 UTC 或 ±HH:MM 固定偏移(不引 tzdata,IANA 名如 Asia/Shanghai 会被拒;
// 有夏令时的时区须按区间换算为固定偏移),label 以北京时间等惯用称呼辅助理解
const TIMEZONES: { value: string; label: string }[] = [
  { value: "UTC", label: "UTC(协调世界时)" },
  { value: "+08:00", label: "UTC+08:00(北京时间)" },
  { value: "+09:00", label: "UTC+09:00(东京/首尔)" },
  { value: "+07:00", label: "UTC+07:00(曼谷/雅加达)" },
  { value: "+05:30", label: "UTC+05:30(印度)" },
  { value: "+00:00", label: "UTC+00:00(伦敦冬令/GMT)" },
  { value: "-05:00", label: "UTC-05:00(美东标准)" },
  { value: "-08:00", label: "UTC-08:00(美西标准)" },
];

// 时段窗结构化编辑:一行 = 开始 + 结束 + 倍率,提交时拼成后端「HH:MM-HH:MM 倍率」格式。
// 结束为 00:00 与后端「24:00=当日末」同义(跨午夜判定 start>end,0000 < start 即到当日末)。
type WinRow = { start: string; end: string; mult: string };

function windowsToRows(wins: PricingEntry["time_windows"]): WinRow[] {
  return (wins ?? []).map((w) => {
    const [start, end] = (w.window ?? "").split("-");
    return { start: start ?? "", end: end ?? "", mult: w.multiplier ?? "" };
  });
}

// 后端倍率契约:最多 4 位小数,取值 (0,10];开始=结束为空窗,拒绝
function rowsToWindows(rows: WinRow[]): { windows: PricingEntry["time_windows"]; error: string } {
  const windows: PricingEntry["time_windows"] = [];
  for (const r of rows) {
    if (!r.start && !r.end && !r.mult) continue; // 整行留空 = 未使用的行,跳过
    if (!r.start || !r.end || !r.mult)
      return { windows: [], error: "时段窗每行的开始、结束、倍率都须填写完整" };
    if (r.start === r.end) return { windows: [], error: "时段窗的开始与结束不能相同" };
    if (!/^\d+(\.\d{1,4})?$/.test(r.mult) || Number(r.mult) <= 0 || Number(r.mult) > 10)
      return { windows: [], error: "倍率须为大于 0 且不超过 10 的数字,最多 4 位小数" };
    windows.push({ window: `${r.start}-${r.end}`, multiplier: r.mult });
  }
  return { windows, error: "" };
}

export default function Pricing() {
  const [entries, setEntries] = useState<PricingEntry[]>([]);
  const [err, setErr] = useState("");
  const [editing, setEditing] = useState<string | null>(null); // 正在编辑的 alias(null=列表态)
  const [form, setForm] = useState<PricingEntry>(EMPTY);
  const [winRows, setWinRows] = useState<WinRow[]>([]);
  const [busy, setBusy] = useState(false); // 保存防重复
  const [removeTarget, setRemoveTarget] = useState<string | null>(null);

  const refresh = useCallback(() => {
    api
      .listPricing()
      .then((r) => {
        setEntries(r.pricing);
        setErr("");
      })
      .catch((e) => setErr(e.message));
  }, []);
  useEffect(refresh, [refresh]);

  const startEdit = (e: PricingEntry) => {
    setEditing(e.alias ?? "");
    setForm({ ...e, time_windows: e.time_windows ?? [] });
    setWinRows(windowsToRows(e.time_windows));
  };
  const startNew = () => {
    setEditing("");
    setForm({ ...EMPTY });
    setWinRows([]);
  };

  const save = async () => {
    const alias = (editing ?? "").trim();
    if (!alias) {
      setErr("模型别名必填");
      return;
    }
    const { windows, error } = rowsToWindows(winRows);
    if (error) {
      setErr(error);
      return;
    }
    setBusy(true);
    try {
      await api.putPricing(alias, {
        currency: form.currency ?? "CNY",
        input: form.input ?? "",
        cached_input: form.cached_input ?? "",
        output: form.output ?? "",
        timezone: form.timezone ?? "UTC",
        time_windows: windows,
      });
      setEditing(null);
      setErr("");
      refresh();
    } catch (e2) {
      setErr((e2 as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (alias: string) => {
    try {
      await api.deletePricing(alias);
      setErr("");
      refresh();
    } catch (e2) {
      setErr((e2 as Error).message);
    }
  };

  // 单价单位随币种标注(后端契约:十进制小数,元|美元 / 百万 Tokens)
  const unit = form.currency === "USD" ? "美元" : "元";
  // 时区下拉候选:现值不在常用列表(如历史 ±HH:MM 偏移)时追加现值,保证可回显
  const tzValue = form.timezone ?? "UTC";
  const tzOptions = TIMEZONES.some((t) => t.value === tzValue)
    ? TIMEZONES
    : [{ value: tzValue, label: tzValue }, ...TIMEZONES];

  if (editing !== null) {
    return (
      <div className="space-y-4">
        {err && <ErrorAlert>错误:{err}</ErrorAlert>}
        <Card>
          <form
            className="grid gap-4 p-4 text-sm"
            onSubmit={(e) => {
              e.preventDefault();
              void save();
            }}
          >
            <FormRow label="别名" htmlFor="pricing-alias">
              {/* 新增态须可输入别名(否则新增流程不可用);编辑已有行时锁定 */}
              <Input
                id="pricing-alias"
                value={editing}
                disabled={editing !== ""}
                placeholder="如 deepseek-chat"
                onChange={(e) => setEditing(e.target.value)}
              />
            </FormRow>
            <FormRow label="币种" htmlFor="pricing-currency" className="w-40">
              <Select
                id="pricing-currency"
                value={form.currency}
                onChange={(e) =>
                  setForm({ ...form, currency: e.target.value as PricingEntry["currency"] })
                }
              >
                <option value="CNY">CNY</option>
                <option value="USD">USD</option>
              </Select>
            </FormRow>
            {/* 单价单位 = 该行币种 / 百万 Tokens(后端 ParsePrice 契约),随币种切换 */}
            <div className="flex flex-wrap gap-2">
              <FormRow
                label={`输入价(${unit}/百万 Tokens)`}
                htmlFor="pricing-input"
                className="w-52"
              >
                <Input
                  id="pricing-input"
                  value={form.input ?? ""}
                  placeholder="0.44"
                  onChange={(e) => setForm({ ...form, input: e.target.value })}
                  required
                />
              </FormRow>
              <FormRow
                label={`缓存命中价(${unit}/百万 Tokens;空=同输入价)`}
                htmlFor="pricing-cached-input"
                className="w-52"
              >
                <Input
                  id="pricing-cached-input"
                  value={form.cached_input ?? ""}
                  placeholder="0.014"
                  onChange={(e) => setForm({ ...form, cached_input: e.target.value })}
                />
              </FormRow>
            </div>
            <div className="flex flex-wrap gap-2">
              <FormRow
                label={`输出价(${unit}/百万 Tokens)`}
                htmlFor="pricing-output"
                className="w-52"
              >
                <Input
                  id="pricing-output"
                  value={form.output ?? ""}
                  placeholder="1.32"
                  onChange={(e) => setForm({ ...form, output: e.target.value })}
                  required
                />
              </FormRow>
              <FormRow label="时段统计时区" htmlFor="pricing-timezone" className="w-56">
                <Select
                  id="pricing-timezone"
                  value={form.timezone ?? "UTC"}
                  onChange={(e) => setForm({ ...form, timezone: e.target.value })}
                >
                  {tzOptions.map((t) => (
                    <option key={t.value} value={t.value}>
                      {t.label}
                    </option>
                  ))}
                </Select>
              </FormRow>
            </div>
            {/* 时段窗为多控件组合行,各自 aria-label 关联,无单一 htmlFor 目标 */}
            <FormRow label="时段窗(按上述时区判定)">
              <div className="space-y-2">
                {winRows.map((row, i) => (
                  <div key={i} className="flex flex-wrap items-center gap-2">
                    <Input
                      type="time"
                      aria-label="开始时刻"
                      className="w-32"
                      value={row.start}
                      onChange={(e) =>
                        setWinRows(
                          winRows.map((r, j) => (j === i ? { ...r, start: e.target.value } : r)),
                        )
                      }
                    />
                    <span className="text-xs text-muted-foreground">至</span>
                    <Input
                      type="time"
                      aria-label="结束时刻"
                      className="w-32"
                      value={row.end}
                      onChange={(e) =>
                        setWinRows(
                          winRows.map((r, j) => (j === i ? { ...r, end: e.target.value } : r)),
                        )
                      }
                    />
                    <Input
                      type="number"
                      step="0.0001"
                      min="0"
                      max="10"
                      aria-label="计价倍率"
                      placeholder="0.5"
                      className="w-28"
                      value={row.mult}
                      onChange={(e) =>
                        setWinRows(
                          winRows.map((r, j) => (j === i ? { ...r, mult: e.target.value } : r)),
                        )
                      }
                    />
                    <Button
                      type="button"
                      variant="ghost-destructive"
                      size="icon-sm"
                      aria-label="删除该时段"
                      onClick={() => setWinRows(winRows.filter((_, j) => j !== i))}
                    >
                      <X className="size-4" aria-hidden />
                    </Button>
                  </div>
                ))}
                <div>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => setWinRows([...winRows, { start: "", end: "", mult: "" }])}
                  >
                    <Plus className="size-4" aria-hidden />
                    添加时段
                  </Button>
                </div>
                <p className="text-xs text-muted-foreground">
                  窗口内请求按倍率计价(0.5 = 半价),倍率最多 4 位小数;开始晚于结束 = 跨午夜 (如
                  16:30-00:30);结束填 00:00 表示到当日末。
                </p>
              </div>
            </FormRow>
            <div className="flex gap-2 justify-end">
              <Button type="button" variant="outline" onClick={() => setEditing(null)}>
                取消
              </Button>
              <Button type="submit" disabled={busy}>
                保存并生效
              </Button>
            </div>
          </form>
        </Card>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {err && <ErrorAlert>错误:{err}</ErrorAlert>}
      <div className="flex justify-end">
        <Button onClick={startNew}>新增价目</Button>
      </div>
      <Table>
        <THead>
          <TR>
            <TH>别名</TH>
            <TH>来源</TH>
            <TH>币种</TH>
            <TH title="单价:本币 / 百万 Tokens">输入价</TH>
            <TH title="单价:本币 / 百万 Tokens">缓存命中价</TH>
            <TH title="单价:本币 / 百万 Tokens">输出价</TH>
            <TH>时段窗</TH>
            <TH>更新时间</TH>
            <TH>
              <span className="sr-only">操作</span>
            </TH>
          </TR>
        </THead>
        <TBody>
          {entries.length === 0 ? (
            <TR>
              <TD colSpan={9}>
                <EmptyState>暂无价目</EmptyState>
              </TD>
            </TR>
          ) : (
            entries.map((p) => (
              <TR key={p.alias}>
                <TD className="font-mono">{p.alias}</TD>
                <TD>
                  <Badge variant={p.source === "db" ? "success" : "secondary"}>
                    {p.source === "db" ? "运行态" : "静态配置价"}
                  </Badge>
                </TD>
                <TD>{p.currency}</TD>
                <TD className="font-mono" title="单价:本币 / 百万 Tokens">
                  {p.input}
                </TD>
                <TD className="font-mono" title="单价:本币 / 百万 Tokens">
                  {p.cached_input || <span className="text-muted-foreground">=输入</span>}
                </TD>
                <TD className="font-mono" title="单价:本币 / 百万 Tokens">
                  {p.output}
                </TD>
                <TD className="font-mono text-xs">
                  {(p.time_windows ?? []).length === 0 ? (
                    <span className="text-muted-foreground">—</span>
                  ) : (
                    (p.time_windows ?? []).map((w, i) => (
                      <div key={`${w.window}-${i}`}>
                        {w.window} ×{w.multiplier}
                      </div>
                    ))
                  )}
                </TD>
                <TD className="text-xs text-muted-foreground">{formatDateTime(p.updated_at)}</TD>
                <TD>
                  <Button
                    variant="ghost"
                    size="xs"
                    onClick={() => p.alias && startEdit({ ...p, alias: p.alias })}
                  >
                    编辑
                  </Button>
                  {p.source === "db" && (
                    <Button
                      variant="ghost-destructive"
                      size="xs"
                      onClick={() => p.alias && setRemoveTarget(p.alias)}
                    >
                      撤销覆盖
                    </Button>
                  )}
                </TD>
              </TR>
            ))
          )}
        </TBody>
      </Table>

      <AlertDialog
        open={removeTarget !== null}
        onOpenChange={(o) => {
          if (!o) setRemoveTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>撤销运行态覆盖</AlertDialogTitle>
            <AlertDialogDescription>
              {removeTarget
                ? `撤销 ${removeTarget} 的运行态覆盖?该别名暂无运行态价格,按配置文件中的静态价计费。`
                : ""}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (!removeTarget) return;
                void remove(removeTarget);
              }}
            >
              撤销覆盖
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
