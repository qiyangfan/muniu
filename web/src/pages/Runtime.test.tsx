import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import Runtime from "./Runtime";
import { api } from "../lib/api";

vi.mock("../lib/api", () => ({
  api: {
    runtimeStatus: vi.fn(),
    runtimeSharedControl: vi.fn(),
    runtimeCommands: vi.fn(),
    runtimeLogs: vi.fn(),
    runtimeExecuteCommand: vi.fn(),
  },
}));

const status = {
  mode: "managed" as const,
  managed: true,
  service_id: "muniu",
  instance_id: "inst-1",
  version: "dev",
  started_at: "",
  observed_at: "",
  service_api_enabled: true,
  snapshot: { version: 3, stale: false, ready: true },
  registration: { state: "ok", last_attempt: "", last_success: "", attempts: 0 },
  commands: { pending: 0, acked: 1, failed: 0 },
};

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.runtimeStatus).mockResolvedValue(status);
  vi.mocked(api.runtimeSharedControl).mockResolvedValue({
    managed: true,
    snapshot: status.snapshot,
    shared: { available: true, version: 3, tenants: 2, pricing: 4, revocations_version: 1 },
  });
  vi.mocked(api.runtimeCommands).mockResolvedValue({
    enabled: true,
    commands: [
      {
        id: "c1",
        action: "cache_purge",
        action_schema_version: 1,
        source: "control",
        status: "acked",
        attempts: 0,
        created_at: "2026-01-01T00:00:00Z",
        result: "purged=2",
      },
    ],
    stats: { pending: 0, acked: 1, failed: 0 },
  });
  vi.mocked(api.runtimeLogs).mockResolvedValue({
    entries: [
      {
        time: "2026-01-01T00:00:00Z",
        level: "warn",
        message: "控制快照拉取失败,保持 last-known",
        attrs: { request_id: "r1", error_detail: "connection refused" },
      },
    ],
  });
  vi.mocked(api.runtimeExecuteCommand).mockResolvedValue({
    id: "c2",
    action: "cache_purge",
    action_schema_version: 1,
    source: "local",
    status: "acked",
    attempts: 0,
    created_at: "",
  });
});

afterEach(cleanup);

it("展示 managed 只读提示与共享控制投影", async () => {
  render(<Runtime />);
  await screen.findByText(/managed 模式：租户\/价目\/拓扑/);
  expect(screen.getByText("快照版本")).toBeTruthy();
  expect(screen.getAllByText("已回执").length).toBeGreaterThan(0);
  // 诊断字段在 attrs 中，必须随日志展示
  expect(await screen.findByText(/error_detail=connection refused/)).toBeTruthy();
});

it("managed 无有效快照时提示数据面 fail-closed", async () => {
  vi.mocked(api.runtimeStatus).mockResolvedValue({
    ...status,
    snapshot: { version: 0, stale: false, ready: false },
    registration: { state: "starting", last_attempt: "", last_success: "", attempts: 0 },
  });
  vi.mocked(api.runtimeSharedControl).mockResolvedValue({
    managed: true,
    snapshot: { version: 0, stale: false, ready: false },
    shared: { available: false },
  });
  render(<Runtime />);
  expect(await screen.findByText(/尚无有效控制快照/)).toBeTruthy();
});

it("本地命令经二次确认后执行", async () => {
  render(<Runtime />);
  await screen.findByText(/本地运行命令/);
  fireEvent.click(screen.getByRole("button", { name: "执行命令" }));
  fireEvent.click(await screen.findByRole("button", { name: "确认执行" }));
  await waitFor(() =>
    expect(api.runtimeExecuteCommand).toHaveBeenCalledWith({
      action: "cache_purge",
      params: { all: true },
      reason: "本地控制台执行",
    }),
  );
});
