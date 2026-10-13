// 密钥页 Key ID 展示/复制行为测试(openspec fix-api-key-binding-identifiers):
// 缺 ID 不回退(prefix/序号不得充当 ID)、签发结果提供 Key ID 复制、secret 无新增持久化。
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
    // 列表显示截短,title 悬停保留全值(桌面表格与移动卡片双形态)
    await vi.waitFor(() => expect(screen.getAllByTitle(uuid).length).toBeGreaterThanOrEqual(1));
    fireEvent.click(screen.getAllByRole("button", { name: "复制 api-key 的 Key ID" })[0]);
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith(uuid));
    expect(writeText).not.toHaveBeenCalledWith("mk-abcdefghijk");

    // 缺 ID 行:明确「ID 不可用」(桌面表格+移动卡片双形态);其复制按钮禁用;
    // prefix 不得出现在 ID 语义位(移动卡片截短除外)
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

    // 徽章三态如实渲染(双形态=桌面表格+移动卡片,同文本成对出现)
    expect((await screen.findAllByText("已吊销")).length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("已停用").length).toBeGreaterThanOrEqual(1);

    // 启用中的 key → 停用;已停用的 key → 启用(patchKey 载荷即契约)
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

    // 吊销终态:启停按钮缺席(启用/停用按钮只属于前两把 key;双形态成对出现 ×2),
    // 吊销按钮三把俱在 ×2
    expect(screen.getAllByRole("button", { name: "停用" }).length).toBe(2);
    expect(screen.getAllByRole("button", { name: "启用" }).length).toBe(2);
    expect(screen.getAllByRole("button", { name: "吊销" }).length).toBe(6);
  });
});
