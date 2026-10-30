// 告警通道投递失败横幅三态:失败中显著警示、已解除/未配置不渲染、加载失败静默降级。
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import Overview from "./Overview";
import { api, type AlertDeliveryStatus } from "../lib/api";

vi.mock("../lib/api", () => ({
  api: {
    usage: vi.fn().mockResolvedValue({ points: [] }),
    cacheStats: vi.fn().mockResolvedValue({ enabled: false, entries: 0, hits: 0, threshold: 0 }),
    alertDeliveryStatus: vi.fn(),
  },
}));

const delivery = (over: Partial<AlertDeliveryStatus>): AlertDeliveryStatus => ({
  configured: true,
  failing: false,
  ...over,
});

describe("总览页投递横幅", () => {
  it("投递失败中渲染警示横幅,含失败事实", async () => {
    vi.mocked(api.alertDeliveryStatus).mockResolvedValue(
      delivery({
        failing: true,
        last_event_key: "balance_tier:p",
        last_severity: "warning",
        last_summary: "渠道 p 余额约可支撑 2 天",
        last_error: "http_status_503",
        failure_count: 3,
        last_failure_at: "2026-10-30T08:00:00Z",
      }),
    );
    render(<Overview />);
    await vi.waitFor(() => expect(screen.getByRole("alert")).toBeTruthy());
    expect(screen.getByText(/告警通知发送失败/)).toBeTruthy();
    expect(screen.getByText(/渠道 p 余额约可支撑 2 天/)).toBeTruthy();
    expect(screen.getByText(/http_status_503/)).toBeTruthy();
  });

  it("已解除与未配置不渲染横幅", async () => {
    vi.mocked(api.alertDeliveryStatus).mockResolvedValue(delivery({ failing: false }));
    const { unmount } = render(<Overview />);
    await vi.waitFor(() => expect(api.alertDeliveryStatus).toHaveBeenCalled());
    expect(screen.queryByText(/告警通知发送失败/)).toBeNull();
    unmount();

    vi.mocked(api.alertDeliveryStatus).mockResolvedValue(delivery({ configured: false }));
    render(<Overview />);
    await vi.waitFor(() =>
      expect(screen.getAllByText(/语义缓存条目/).length).toBeGreaterThanOrEqual(1),
    );
    expect(screen.queryByText(/告警通知发送失败/)).toBeNull();
  });

  it("投递状态加载失败静默降级,不挡总览", async () => {
    vi.mocked(api.alertDeliveryStatus).mockRejectedValue(new Error("boom"));
    render(<Overview />);
    await vi.waitFor(() =>
      expect(screen.getAllByText(/语义缓存条目/).length).toBeGreaterThanOrEqual(1),
    );
    expect(screen.queryByText(/告警通知发送失败/)).toBeNull();
  });
});
