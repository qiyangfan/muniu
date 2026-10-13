import { act, cleanup, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import Audit from "./Audit";
import { api, type AdminAudit } from "../lib/api";
vi.mock("../lib/api", () => ({ api: { audit: vi.fn() } }));
afterEach(cleanup);
beforeEach(() => {
  vi.clearAllMocks();
});
const row = (id: string): AdminAudit => ({
  id,
  actor_kind: "session",
  actor_name: id,
  action: "keys.patch",
  outcome: "ok",
  created_at: "2026-10-13T00:00:00Z",
  detail: '{"max_request_cost_credits":9007199254740993}',
});
it("审计整数详情保留原始十进制值", async () => {
  vi.mocked(api.audit).mockResolvedValue({ items: [row("alice")], next_cursor: "" });
  render(<Audit />);
  await screen.findAllByText("alice");
  expect(screen.getAllByText(/9007199254740993/).length).toBeGreaterThan(0);
});
it("StrictMode的迟到首屏不能覆盖已加载后页", async () => {
  let resolveOld!: (p: { items: AdminAudit[]; next_cursor: string }) => void;
  vi.mocked(api.audit)
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveOld = resolve;
        }),
    )
    .mockResolvedValueOnce({ items: [row("first")], next_cursor: "cursor" })
    .mockResolvedValueOnce({ items: [row("second")], next_cursor: "" });
  render(
    <StrictMode>
      <Audit />
    </StrictMode>,
  );
  await screen.findAllByText("first");
  await waitFor(() =>
    expect((screen.getByRole("button", { name: "加载更多" }) as HTMLButtonElement).disabled).toBe(
      false,
    ),
  );
  fireEvent.click(screen.getByRole("button", { name: "加载更多" }));
  await screen.findAllByText("second");
  await act(async () => {
    resolveOld({ items: [row("old")], next_cursor: "cursor" });
  });
  expect(screen.getAllByText("second").length).toBeGreaterThan(0);
  expect(screen.queryAllByText("old").length).toBe(0);
});
