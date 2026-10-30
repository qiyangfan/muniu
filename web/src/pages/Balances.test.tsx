// 覆盖:未启用空态、估算降级文案、告警徽章、manual 录入载荷与刷新调用。
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

import Balances from "./Balances";
import { api, type BalanceOverview, type BalancesResponse } from "../lib/api";

vi.mock("../lib/api", () => ({
  api: {
    balances: vi.fn(),
    balanceManual: vi.fn(),
    balanceRefresh: vi.fn(),
  },
}));

const overview = (over: Partial<BalanceOverview>): BalanceOverview => ({
  provider: "ch",
  source: "manual",
  below: "",
  currency: "CNY",
  manual_currency: "CNY",
  min_effective: "30",
  burn_per_day_credits: 10_000_000_000,
  burn_window_days: 7,
  days_with_usage: 7,
  remaining_hours: 72,
  estimate_reason: "",
  keys: [],
  alerts: [],
  last_checked_at: "2026-10-29T12:00:00Z",
  ...over,
});

function renderPage(resp: BalancesResponse) {
  vi.mocked(api.balances).mockResolvedValue(resp);
  render(<Balances />);
}

describe("余额告警页", () => {
  it("未启用时呈现配置指引空态,不渲染录入表单", async () => {
    renderPage({ enabled: false, providers: [] });
    await vi.waitFor(() =>
      expect(screen.getAllByText(/余额观测未启用/).length).toBeGreaterThanOrEqual(1),
    );
    expect(screen.queryByText("手动录入余额快照")).toBeNull();
  });

  it("有估算时展示「约 N 天」,降级原因直出用户事实文案", async () => {
    renderPage({
      enabled: true,
      settlement: "CNY",
      providers: [
        overview({ provider: "ch", remaining_hours: 72 }),
        overview({
          provider: "usd-ch",
          source: "openrouter",
          currency: "USD",
          remaining_hours: null,
          estimate_reason: "currency_mismatch",
        }),
        overview({
          provider: "idle",
          remaining_hours: null,
          estimate_reason: "no_usage",
        }),
      ],
    });
    await vi.waitFor(() => expect(screen.getAllByText("约 3 天").length).toBeGreaterThanOrEqual(1));
    // jsdom 同时渲染桌面表格与移动卡片,同一文案出现两份属预期
    expect(
      screen.getAllByText("余额币种与本位币不一致,无法估算天数").length,
    ).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("无消耗,剩余不限").length).toBeGreaterThanOrEqual(1);
  });

  it("firing 告警渲染严重级徽章(档位转人类表述),非 firing 不展示", async () => {
    renderPage({
      enabled: true,
      providers: [
        overview({
          alerts: [
            { rule_key: "tier:24", firing: true, severity: "critical", last_fired_at: null },
            { rule_key: "below", firing: false, severity: "critical", last_fired_at: null },
          ],
        }),
      ],
    });
    await vi.waitFor(() => expect(screen.getAllByText("剩 1 天").length).toBeGreaterThanOrEqual(1));
    expect(screen.queryByText("低于阈值")).toBeNull();
  });

  it("异常消耗规则徽章文案:burn_spike 与 cliff:<key_id> 转中文标签", async () => {
    renderPage({
      enabled: true,
      providers: [
        overview({
          alerts: [
            { rule_key: "burn_spike", firing: true, severity: "warning", last_fired_at: null },
            { rule_key: "cliff:k1", firing: true, severity: "warning", last_fired_at: null },
          ],
        }),
      ],
    });
    await vi.waitFor(() =>
      expect(screen.getAllByText("消耗突增").length).toBeGreaterThanOrEqual(1),
    );
    expect(screen.getAllByText("余额骤降").length).toBeGreaterThanOrEqual(1);
  });

  it("manual 录入以渠道声明币种提交载荷", async () => {
    vi.mocked(api.balanceManual).mockResolvedValueOnce({ ok: true });
    renderPage({ enabled: true, providers: [overview({})] });
    await vi.waitFor(() => expect(screen.getByLabelText("余额(CNY)")).toBeTruthy());
    fireEvent.change(screen.getByLabelText("余额(CNY)"), { target: { value: "128.50" } });
    fireEvent.click(screen.getByRole("button", { name: "录入快照" }));
    await vi.waitFor(() =>
      expect(api.balanceManual).toHaveBeenCalledWith("ch", {
        amount: "128.50",
        currency: "CNY",
      }),
    );
  });

  it("manual 录入失败时界面透出后端 message", async () => {
    vi.mocked(api.balanceManual).mockRejectedValueOnce(new Error("金额非法"));
    renderPage({ enabled: true, providers: [overview({})] });
    await vi.waitFor(() => expect(screen.getByLabelText("余额(CNY)")).toBeTruthy());
    fireEvent.change(screen.getByLabelText("余额(CNY)"), { target: { value: "0" } });
    fireEvent.click(screen.getAllByRole("button", { name: "录入快照" })[0]);
    await vi.waitFor(() =>
      expect(screen.getAllByText("金额非法").length).toBeGreaterThanOrEqual(1),
    );
  });

  it("刷新按钮调用渠道 refresh 端点", async () => {
    vi.mocked(api.balanceRefresh).mockResolvedValue({ ok: true });
    renderPage({ enabled: true, providers: [overview({ provider: "dsx", source: "deepseek" })] });
    // 桌面表格与移动卡片各渲染一份操作按钮,任一可点即视为通过
    await vi.waitFor(() =>
      expect(
        screen.getAllByRole("button", { name: "刷新 dsx 的余额" }).length,
      ).toBeGreaterThanOrEqual(1),
    );
    fireEvent.click(screen.getAllByRole("button", { name: "刷新 dsx 的余额" })[0]);
    await vi.waitFor(() => expect(api.balanceRefresh).toHaveBeenCalledWith("dsx"));
  });

  it("日均消耗按精确十进制串渲染(零浮点尾差)", async () => {
    renderPage({
      enabled: true,
      providers: [overview({ burn_per_day_credits: 2_857_142_857 })],
    });
    await vi.waitFor(() =>
      expect(screen.getAllByText("≈2.857142857").length).toBeGreaterThanOrEqual(1),
    );
  });
});

it("manual 渠道收窄后录入表单回退首个可用渠道,列表恢复后原选中复活", async () => {
  vi.mocked(api.balanceManual).mockResolvedValue({ ok: true });
  // 三轮取数(双渠道 → 收窄至 ch2 → 恢复双渠道),每轮由一次录入提交触发真实 reload;
  // ch2 币种取 USD 使回退/复活在币种标注上可观测
  vi.mocked(api.balances)
    .mockResolvedValueOnce({
      enabled: true,
      providers: [
        overview({ provider: "ch" }),
        overview({ provider: "ch2", manual_currency: "USD" }),
      ],
    })
    .mockResolvedValueOnce({
      enabled: true,
      providers: [overview({ provider: "ch2", manual_currency: "USD" })],
    })
    // 恢复轮 ch 刻意排第二位:「恒取首个」的实现下选中会停在 ch2,只有 picked 存续才能复活 ch
    .mockResolvedValueOnce({
      enabled: true,
      providers: [
        overview({ provider: "ch2", manual_currency: "USD" }),
        overview({ provider: "ch" }),
      ],
    });
  render(<Balances />);
  await vi.waitFor(() => expect(screen.queryByText("余额(CNY)")).toBeTruthy());
  const submit = async (currency: string, balancesCalls: number) => {
    fireEvent.change(screen.getByLabelText(`余额(${currency})`), { target: { value: "12.00" } });
    fireEvent.click(screen.getAllByRole("button", { name: "录入快照" })[0]);
    await vi.waitFor(() => expect(api.balances).toHaveBeenCalledTimes(balancesCalls));
  };
  // 提交触发 reload → 列表收窄至 ch2:选中回退到 ch2(币种标注随之切换)
  await submit("CNY", 2);
  await vi.waitFor(() => expect(screen.queryByText("余额(USD)")).toBeTruthy());
  expect(screen.queryByText("余额(CNY)")).toBeNull();
  // 再次提交 → reload 恢复双渠道:原选中 ch 复活(派生值随列表恢复)
  await submit("USD", 3);
  await vi.waitFor(() => expect(screen.queryByText("余额(CNY)")).toBeTruthy());
  expect(screen.queryByText("余额(USD)")).toBeNull();
});
