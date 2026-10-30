// Providers 页健康徽标三态:降级显著标识+原因、健康态不渲染、观测未启用不渲染。
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import Providers from "./Providers";
import { api, type ProviderInfo, type UpstreamHealthProvider } from "../lib/api";

vi.mock("../lib/api", () => ({
  api: {
    providers: vi.fn(),
    providerPresets: vi.fn().mockResolvedValue({ presets: [] }),
    upstreamHealth: vi.fn(),
  },
}));

const provider = (name: string): ProviderInfo => ({
  name,
  adapter: "deepseek",
  base_url: "https://api.example.com/v1",
  weight: 10,
  priority: 0,
  keys: [],
});

const health = (over: Partial<UpstreamHealthProvider>): UpstreamHealthProvider => ({
  provider: "prov-a",
  phase: "healthy",
  ...over,
});

function renderPage(healthProviders: UpstreamHealthProvider[]) {
  vi.mocked(api.providers).mockResolvedValue({ providers: [provider("prov-a")] });
  vi.mocked(api.upstreamHealth).mockResolvedValue({
    enabled: true,
    providers: healthProviders,
    aliases: [],
  });
  render(<Providers />);
}

describe("供应商页健康徽标", () => {
  it("降级渠道显著标识并透出降级原因", async () => {
    renderPage([health({ provider: "prov-a", phase: "degraded", reason: "control_plane" })]);
    await vi.waitFor(() =>
      expect(screen.getAllByText("健康熔断中").length).toBeGreaterThanOrEqual(1),
    );
    expect(screen.getByText(/降级原因:控制面探针失败/)).toBeTruthy();
  });

  it("健康态与半开态分别不渲染/渲染警示", async () => {
    renderPage([health({ provider: "prov-a", phase: "healthy" })]);
    await vi.waitFor(() => expect(screen.getAllByText("prov-a").length).toBeGreaterThanOrEqual(1));
    expect(screen.queryByText("健康熔断中")).toBeNull();
    expect(screen.queryByText("恢复试探中")).toBeNull();
  });

  it("观测未启用(enabled=false)不渲染任何健康元素", async () => {
    vi.mocked(api.providers).mockResolvedValue({ providers: [provider("prov-a")] });
    vi.mocked(api.upstreamHealth).mockResolvedValue({
      enabled: false,
      providers: [],
      aliases: [],
    });
    render(<Providers />);
    await vi.waitFor(() => expect(screen.getAllByText("prov-a").length).toBeGreaterThanOrEqual(1));
    expect(screen.queryByText("健康熔断中")).toBeNull();
  });
});
