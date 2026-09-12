// 价目管理:在线改价(动态价目,metering.md「动态价目」)——列表/编辑/撤销覆盖。
// 价目为运营态数据:改价即时生效零重启;yaml 兜底行编辑即建 DB 覆盖。
import { useCallback, useEffect, useState } from "react";
import { api, type PricingEntry } from "../lib/api";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Select } from "../components/ui/select";
import { Textarea } from "../components/ui/textarea";
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

const EMPTY: PricingEntry = {
  currency: "CNY",
  input: "",
  cached_input: "",
  output: "",
  timezone: "UTC",
  time_windows: [],
};

// 时段窗编辑采用行文本形态(每行「窗口 倍率」),避免内联表单复杂度:
//   16:30-00:30 0.5
function windowsToText(wins: PricingEntry["time_windows"]): string {
  return (wins ?? []).map((w) => `${w.window} ${w.multiplier}`).join("\n");
}
function textToWindows(text: string): { windows: PricingEntry["time_windows"]; bad: boolean } {
  let bad = false;
  const windows = text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "")
    .map((line) => {
      const parts = line.split(/\s+/);
      if (parts.length !== 2) {
        bad = true;
        return { window: "", multiplier: "" };
      }
      return { window: parts[0], multiplier: parts[1] };
    });
  return { windows, bad };
}

export default function Pricing() {
  const [entries, setEntries] = useState<PricingEntry[]>([]);
  const [err, setErr] = useState("");
  const [editing, setEditing] = useState<string | null>(null); // 正在编辑的 alias(null=列表态)
  const [form, setForm] = useState<PricingEntry>(EMPTY);
  const [winText, setWinText] = useState("");
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
    setWinText(windowsToText(e.time_windows));
  };
  const startNew = () => {
    setEditing("");
    setForm({ ...EMPTY });
    setWinText("");
  };

  const save = async () => {
    const alias = (editing ?? "").trim();
    if (!alias) {
      setErr("alias 必填");
      return;
    }
    const { windows, bad } = textToWindows(winText);
    if (bad) {
      setErr("时段窗每行须为「窗口 倍率」两段,如 16:30-00:30 0.5");
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
                placeholder="model 别名"
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
            <div className="flex flex-wrap gap-2">
              <FormRow label="输入价" htmlFor="pricing-input" className="w-40">
                <Input
                  id="pricing-input"
                  value={form.input ?? ""}
                  placeholder="0.44"
                  onChange={(e) => setForm({ ...form, input: e.target.value })}
                  required
                />
              </FormRow>
              <FormRow
                label="缓存命中价(空=同输入价)"
                htmlFor="pricing-cached-input"
                className="w-40"
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
              <FormRow label="输出价" htmlFor="pricing-output" className="w-40">
                <Input
                  id="pricing-output"
                  value={form.output ?? ""}
                  placeholder="1.32"
                  onChange={(e) => setForm({ ...form, output: e.target.value })}
                  required
                />
              </FormRow>
              <FormRow label="时区" htmlFor="pricing-timezone" className="w-40">
                <Input
                  id="pricing-timezone"
                  value={form.timezone ?? "UTC"}
                  placeholder="UTC / +08:00"
                  onChange={(e) => setForm({ ...form, timezone: e.target.value })}
                />
              </FormRow>
            </div>
            <FormRow label="时段窗" htmlFor="pricing-time-windows">
              <Textarea
                id="pricing-time-windows"
                className="font-mono"
                rows={3}
                value={winText}
                placeholder={"每行一条:窗口 倍率\n16:30-00:30 0.5"}
                onChange={(e) => setWinText(e.target.value)}
              />
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
            <TH>输入</TH>
            <TH>缓存命中</TH>
            <TH>输出</TH>
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
                    {p.source === "db" ? "运行态" : "yaml 兜底"}
                  </Badge>
                </TD>
                <TD>{p.currency}</TD>
                <TD className="font-mono">{p.input}</TD>
                <TD className="font-mono">
                  {p.cached_input || <span className="text-muted-foreground">=输入</span>}
                </TD>
                <TD className="font-mono">{p.output}</TD>
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
                <TD className="text-xs text-muted-foreground">{p.updated_at || "—"}</TD>
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
              {removeTarget ? `撤销 ${removeTarget} 的运行态覆盖?该别名将回落 YAML 静态价。` : ""}
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
