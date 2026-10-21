// 复制值取原始完整值、失败反馈保留可手动选择的文本、禁用不触发复制、无新增持久化。
import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";

import { CopyButton } from "./copy-button";

const REAL_UUID = "0192f3a1-6c8b-7d2e-9a4f-5b6c7d8e9f0a";
const LONG_CUSTOM_ID = "platform-key-with-a-very-long-custom-string-id-0123456789abcdef";

// jsdom 无剪贴板实现;stub 成可编程 mock(每用例重建)
function stubClipboard() {
  const writeText = vi.fn<(text: string) => Promise<void>>();
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText },
    configurable: true,
  });
  return writeText;
}

// 存储断言:secret/ID 复制流程不得引入任何新的浏览器持久化
function expectNoPersistentStorage() {
  expect(sessionStorage.length).toBe(0);
  expect(localStorage.length).toBe(0);
}

describe("CopyButton", () => {
  it("复制真实 Key ID:剪贴板获得完整原值,与展示无关", async () => {
    const writeText = stubClipboard().mockResolvedValue(undefined);
    render(<CopyButton value={REAL_UUID} label="复制 ID" />);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "复制 ID" }));
    });
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    expect(writeText).toHaveBeenCalledWith(REAL_UUID);
    expectNoPersistentStorage();
  });

  it("长自定义字符串 ID 完整复制:不截断、不转换大小写、不加前缀", async () => {
    const writeText = stubClipboard().mockResolvedValue(undefined);
    render(<CopyButton value={LONG_CUSTOM_ID} label="复制 ID" />);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "复制 ID" }));
    });
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    // 逐字相等即证明无任何形态改写(非 mk- 前缀/非 UUID 的合法自定义 ID 同样原样)
    expect(writeText).toHaveBeenCalledWith(LONG_CUSTOM_ID);
    expect(writeText).not.toHaveBeenCalledWith(expect.stringContaining("mk-"));
  });

  it("复制失败给出反馈,展示文本保留可手动选择", async () => {
    const writeText = stubClipboard().mockRejectedValue(new Error("denied"));
    render(
      <span>
        <code>{REAL_UUID}</code>
        <CopyButton value={REAL_UUID} label="复制 ID" failHint="复制失败,请手动复制" />
      </span>,
    );
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "复制 ID" }));
    });
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("复制失败,请手动复制");
    // 值文本仍在文档中(可手动选中),失败反馈不吞原值
    expect(screen.getByText(REAL_UUID)).toBeDefined();
    expectNoPersistentStorage();
  });

  it("禁用态不触发复制(缺 ID 场景由调用方展示占位并禁用)", () => {
    const writeText = stubClipboard();
    render(<CopyButton value="" label="复制 ID" disabled />);
    const btn = screen.getByRole("button", { name: "复制 ID" }) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    fireEvent.click(btn);
    expect(writeText).not.toHaveBeenCalled();
  });
});
