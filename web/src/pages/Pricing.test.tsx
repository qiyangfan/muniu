import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import Pricing from "./Pricing";
import { api, type PricingEntry } from "../lib/api";

vi.mock("../lib/api", () => ({
  api: { listPricing: vi.fn(), putPricing: vi.fn(), deletePricing: vi.fn() },
}));
vi.mock("./ImagePricing", () => ({ default: () => null }));

afterEach(cleanup);

it("编辑并保存视觉输入价", async () => {
  const entry: PricingEntry = {
    alias: "qwen-vl",
    source: "yaml",
    currency: "CNY",
    input: "1",
    cached_input: "",
    output: "2",
    timezone: "UTC",
    time_windows: [],
  };
  vi.mocked(api.listPricing).mockResolvedValue({ pricing: [entry] });
  vi.mocked(api.putPricing).mockResolvedValue({ ok: true });

  render(<Pricing />);
  fireEvent.click((await screen.findAllByRole("button", { name: "编辑" }))[0]);
  fireEvent.change(screen.getByLabelText(/视觉输入价/), { target: { value: "3.5" } });
  fireEvent.click(screen.getByRole("button", { name: "保存并生效" }));

  expect(api.putPricing).toHaveBeenCalledWith(
    "qwen-vl",
    expect.objectContaining({ vision_input: "3.5" }),
  );
});
