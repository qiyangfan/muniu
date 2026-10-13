import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import ImagePricing from "./ImagePricing";
import { api, type ImagePricingEntry } from "../lib/api";
vi.mock("../lib/api", () => ({
  api: { listImagePricing: vi.fn(), putImagePricing: vi.fn(), deleteImagePricing: vi.fn() },
}));
afterEach(cleanup);
it("保存后的新价目不被迟到首屏覆盖", async () => {
  let finish!: (r: { pricing: ImagePricingEntry[] }) => void;
  vi.mocked(api.listImagePricing)
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    )
    .mockResolvedValue({
      pricing: [{ alias: "paint", source: "db", currency: "CNY", default: "0.4", tiers: {} }],
    });
  vi.mocked(api.putImagePricing).mockResolvedValue({ ok: true });
  render(<ImagePricing />);
  fireEvent.click(screen.getByRole("button", { name: "新增图片价目" }));
  fireEvent.change(screen.getByLabelText("模型别名"), { target: { value: "paint" } });
  fireEvent.change(screen.getByLabelText("每张兜底价"), { target: { value: "0.4" } });
  fireEvent.click(screen.getByRole("button", { name: "保存" }));
  await screen.findAllByText("运行态覆盖");
  await act(async () => {
    finish({ pricing: [] });
  });
  expect(screen.getAllByText("运行态覆盖").length).toBeGreaterThan(0);
});
