// 覆盖 Key ID 展示、复制、缺失处理和签发后的明文保密。
import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";

import Keys from "./Keys";
import { api, type KeyRecord } from "../lib/api";

vi.mock("../lib/api", () => ({
  api: {
    listKeys: vi.fn(),
    createKey: vi.fn(),
    patchKey: vi.fn(),
    revokeKey: vi.fn(),
  },
}));

const keyWithID = (over: Partial<KeyRecord>): KeyRecord => ({
  id: "0192f3a1-6c8b-7d2e-9a4f-5b6c7d8e9f0a",
  tenant_id: "default",
  name: "api-key",
  prefix: "mk-abcdefghijk",
  rpm_limit: 60,
  tpm_limit: 0,
  daily_quota_credits: 0,
  expires_at: null,
  allowed_models: null,
  ...over,
});

function stubClipboard() {
  const writeText = vi.fn<(text: string) => Promise<void>>();
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText },
    configurable: true,
  });
  return writeText;
}

describe("密钥页 Key ID", () => {
  it("列表展示真实 Key ID 并按完整原值复制;缺 ID 条目显示不可用且禁用复制,不以 prefix 回退", async () => {
    const writeText = stubClipboard().mockResolvedValue(undefined);
    vi.mocked(api.listKeys).mockResolvedValue({
      keys: [keyWithID({}), keyWithID({ id: undefined, prefix: "mk-fallback-no", name: "legacy" })],
    });
    render(<Keys />);

    const uuid = "0192f3a1-6c8b-7d2e-9a4f-5b6c7d8e9f0a";
    await vi.waitFor(() => expect(screen.getAllByTitle(uuid).length).toBeGreaterThanOrEqual(1));
    fireEvent.click(screen.getAllByRole("button", { name: "复制 api-key 的 Key ID" })[0]);
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith(uuid));
    expect(writeText).not.toHaveBeenCalledWith("mk-abcdefghijk");

    // 缺失 ID 不得回退到 prefix。
    const unavailable = await screen.findAllByText("ID 不可用");
    expect(unavailable.length).toBeGreaterThanOrEqual(1);
    const legacyBtn = screen.getAllByRole("button", {
      name: "复制 legacy 的 Key ID",
    })[0] as HTMLButtonElement;
    expect(legacyBtn.disabled).toBe(true);
    expect(sessionStorage.length).toBe(0);
    expect(localStorage.length).toBe(0);
  });

  it("签发结果同时展示明文与 Key ID;复制 Key ID 用响应 id,明文不进任何持久化", async () => {
    const writeText = stubClipboard().mockResolvedValue(undefined);
    vi.mocked(api.listKeys).mockResolvedValue({ keys: [] });
    const issuedID = "0192ab12-3456-7890-abcd-ef0123456789";
    vi.mocked(api.createKey).mockResolvedValue({
      key: "mk-testsecret-testsecret-testsecret",
      id: issuedID,
      prefix: "mk-testsecret",
      tenant: "default",
      rpm_limit: 60,
      tpm_limit: 0,
      daily_quota_credits: 0,
      expires_at: null,
      allowed_models: null,
    });
    render(<Keys />);

    fireEvent.change(await screen.findByLabelText("key 名称"), {
      target: { value: "e2e" },
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "签发" }));
    });
    await screen.findByText(issuedID);
    expect(screen.getByText(/明文仅此一次/)).toBeDefined();

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "复制 Key ID" }));
    });
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith(issuedID));
    expect(writeText).not.toHaveBeenCalledWith("mk-testsecret-testsecret-testsecret");
    expect(sessionStorage.length).toBe(0);
    expect(localStorage.length).toBe(0);
  });
});

describe("密钥页启停与状态徽章", () => {
  it("状态徽章三态可区分;启停按钮按当前状态调用 patchKey;吊销 key 不提供启停", async () => {
    vi.mocked(api.listKeys).mockResolvedValue({
      keys: [
        keyWithID({ name: "active-key", status: "active", disabled: false }),
        keyWithID({
          id: "0192f3a1-6c8b-7d2e-9a4f-000000000001",
          prefix: "mk-stoppedkey12",
          name: "stopped-key",
          status: "active",
          disabled: true,
        }),
        keyWithID({
          id: "0192f3a1-6c8b-7d2e-9a4f-000000000002",
          prefix: "mk-revokedkey1",
          name: "revoked-key",
          status: "revoked",
          disabled: false,
        }),
      ],
    });
    vi.mocked(api.patchKey).mockResolvedValue(keyWithID({}));
    render(<Keys />);

    expect((await screen.findAllByText("已吊销")).length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("已停用").length).toBeGreaterThanOrEqual(1);

    fireEvent.click(screen.getAllByRole("button", { name: "停用" })[0]);
    await vi.waitFor(() =>
      expect(api.patchKey).toHaveBeenCalledWith("0192f3a1-6c8b-7d2e-9a4f-5b6c7d8e9f0a", {
        disabled: true,
      }),
    );
    fireEvent.click(screen.getAllByRole("button", { name: "启用" })[0]);
    await vi.waitFor(() =>
      expect(api.patchKey).toHaveBeenCalledWith("0192f3a1-6c8b-7d2e-9a4f-000000000001", {
        disabled: false,
      }),
    );

    // 吊销状态不可启停；每条记录在桌面和移动视图各渲染一次。
    expect(screen.getAllByRole("button", { name: "停用" }).length).toBe(2);
    expect(screen.getAllByRole("button", { name: "启用" }).length).toBe(2);
    expect(screen.getAllByRole("button", { name: "吊销" }).length).toBe(6);
  });
});
