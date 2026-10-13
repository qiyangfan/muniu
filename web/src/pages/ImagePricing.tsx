import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../components/ui/select";
import { useCallback, useEffect, useRef, useState } from "react";
import { api, type ImagePricingEntry } from "../lib/api";
import { DataList, type DataCol } from "../components/data-list";
import { ErrorAlert } from "../components/error-alert";
import { FormRow } from "../components/form-row";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Textarea } from "../components/ui/textarea";
import { Card, CardHeader, CardTitle, CardContent } from "../components/ui/card";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
  AlertDialogAction,
} from "../components/ui/alert-dialog";

export default function ImagePricing() {
  const version = useRef(0);
  const [rows, setRows] = useState<ImagePricingEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState("");
  const [editing, setEditing] = useState<string | null>(null);
  const [currency, setCurrency] = useState<"CNY" | "USD">("CNY");
  const [def, setDef] = useState("");
  const [tiers, setTiers] = useState("{}");
  const [busy, setBusy] = useState(false);
  const [remove, setRemove] = useState<string | null>(null);
  const refresh = useCallback(() => {
    const current = ++version.current;
    setLoading(true);
    api
      .listImagePricing()
      .then((r) => {
        if (current !== version.current) return;
        setRows(r.pricing);
        setErr("");
      })
      .catch((e) => {
        if (current === version.current) setErr(e.message);
      })
      .finally(() => {
        if (current === version.current) setLoading(false);
      });
  }, []);
  useEffect(() => {
    refresh();
    return () => {
      version.current++;
    };
  }, [refresh]);
  const edit = (r?: ImagePricingEntry) => {
    setEditing(r?.alias ?? "");
    setCurrency(r?.currency ?? "CNY");
    setDef(r?.default ?? "");
    setTiers(JSON.stringify(r?.tiers ?? {}, null, 2));
  };
  const cols: DataCol<ImagePricingEntry>[] = [
    { key: "alias", label: "模型别名", primary: true, render: (r) => r.alias },
    {
      key: "source",
      label: "来源",
      render: (r) => (r.source === "db" ? "运行态覆盖" : "静态兜底"),
    },
    { key: "currency", label: "币种", render: (r) => r.currency },
    { key: "default", label: "每张兜底价", numeric: true, render: (r) => r.default },
    {
      key: "tiers",
      label: "档位报价",
      full: true,
      render: (r) =>
        Object.entries(r.tiers ?? {})
          .map(([k, v]) => `${k}: ${v}`)
          .join("；") || "无档位，使用兜底价",
    },
    {
      key: "actions",
      label: "操作",
      actions: true,
      render: (r, mobile) => (
        <>
          <Button size={mobile ? "sm" : "xs"} variant="outline" onClick={() => edit(r)}>
            编辑
          </Button>
          {r.source === "db" && (
            <Button
              size={mobile ? "sm" : "xs"}
              variant="ghost-destructive"
              onClick={() => setRemove(r.alias)}
            >
              撤销覆盖
            </Button>
          )}
        </>
      ),
    },
  ];
  return (
    <Card>
      <CardHeader>
        <CardTitle>图片生成价目</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">
          按张计价，size|quality 档位未命中时使用兜底单价。
        </p>
        {err && <ErrorAlert text={err} />}
        <Button size="sm" variant="outline" onClick={() => edit()}>
          新增图片价目
        </Button>
        {editing !== null && (
          <form
            className="grid grid-cols-1 gap-4 sm:grid-cols-2"
            onSubmit={async (e) => {
              e.preventDefault();
              setBusy(true);
              setErr("");
              try {
                const parsed: unknown = JSON.parse(tiers);
                if (
                  !parsed ||
                  typeof parsed !== "object" ||
                  Array.isArray(parsed) ||
                  Object.values(parsed).some((v) => typeof v !== "string")
                )
                  throw new Error("档位须为 JSON 对象，单价须写成字符串");
                await api.putImagePricing(editing.trim(), {
                  alias: editing.trim(),
                  currency,
                  default: def,
                  tiers: parsed as Record<string, string>,
                });
                setEditing(null);
                refresh();
              } catch (e2) {
                setErr((e2 as Error).message);
              } finally {
                setBusy(false);
              }
            }}
          >
            <FormRow label="模型别名" htmlFor="image-alias">
              <Input
                id="image-alias"
                value={editing}
                onChange={(e) => setEditing(e.target.value)}
                required
              />
            </FormRow>
            <FormRow label="币种（CNY 或 USD）" htmlFor="image-currency">
              <Select
                value={currency}
                onValueChange={(value) => {
                  if (value === "CNY" || value === "USD") setCurrency(value);
                }}
              >
                <SelectTrigger id="image-currency">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="CNY">CNY</SelectItem>
                  <SelectItem value="USD">USD</SelectItem>
                </SelectContent>
              </Select>
            </FormRow>
            <FormRow label="每张兜底价" htmlFor="image-default">
              <Input
                id="image-default"
                value={def}
                onChange={(e) => setDef(e.target.value)}
                inputMode="decimal"
                required
              />
            </FormRow>
            <div className="sm:col-span-2">
              <FormRow label={'档位报价（示例：{"1024x1024|hd":"0.3"}）'} htmlFor="image-tiers">
                <Textarea
                  id="image-tiers"
                  value={tiers}
                  onChange={(e) => setTiers(e.target.value)}
                  rows={4}
                />
              </FormRow>
            </div>
            <div className="flex gap-2">
              <Button type="submit" disabled={busy}>
                {busy ? "保存中…" : "保存"}
              </Button>
              <Button type="button" variant="outline" onClick={() => setEditing(null)}>
                取消
              </Button>
            </div>
          </form>
        )}
        <DataList
          rows={rows}
          columns={cols}
          rowKey={(r) => r.alias}
          empty="暂无图片生成价目"
          loading={loading}
        />
        <AlertDialog
          open={remove !== null}
          onOpenChange={(v) => {
            if (!v) setRemove(null);
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>撤销图片价目覆盖</AlertDialogTitle>
              <AlertDialogDescription>
                撤销 {remove} 的运行态价格后，将使用静态兜底价；无兜底价时记零成本。
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>取消</AlertDialogCancel>
              <AlertDialogAction
                variant="destructive"
                onClick={() => {
                  if (!remove) return;
                  void api
                    .deleteImagePricing(remove)
                    .then(() => {
                      setRemove(null);
                      refresh();
                    })
                    .catch((e) => setErr(e.message));
                }}
              >
                撤销覆盖
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </CardContent>
    </Card>
  );
}
