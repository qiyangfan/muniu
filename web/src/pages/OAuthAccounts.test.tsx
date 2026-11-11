// 覆盖:空态指引、凭据披露(录入对话框内,无常驻横幅)、状态徽章映射、删除确认链路、
// 行级授权按钮的 flow_configured 门控、双形态渲染锁定。
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";

import OAuthAccounts from "./OAuthAccounts";
import { api, type OAuthAccountView, type OAuthAccountsResponse } from "../lib/api";

vi.mock("../lib/api", () => ({
  api: {
    oauthAccounts: vi.fn(),
    oauthAccountCreate: vi.fn(),
    oauthAccountDelete: vi.fn(),
    oauthAuthorize: vi.fn(),
  },
}));

// window.open 在 jsdom 无实现,授权按钮路径 mock 掉避免噪声。
vi.spyOn(window, "open").mockImplementation(() => null);

const account = (over: Partial<OAuthAccountView>): OAuthAccountView => ({
  id: "acc-1",
  provider_type: "claude",
  status: "active",
  status_reason: "",
  expires_at: "2026-11-11T12:00:00Z",
  access_token_masked: "sk-sec…(26 字符)",
  refresh_present: true,
  ...over,
});

function renderPage(resp: OAuthAccountsResponse) {
  vi.mocked(api.oauthAccounts).mockResolvedValue(resp);
  render(<OAuthAccounts />);
}

describe("订阅账号页", () => {
  it("空账号呈现录入指引;明文落库披露在录入对话框内,无常驻横幅", async () => {
    renderPage({
      accounts: [],
      meta: {
        flow_configured: false,
        plaintext_disclosure: "refresh token 以明文存储于本地数据库",
      },
    });
    await vi.waitFor(() =>
      expect(screen.getAllByText(/暂无订阅账号/).length).toBeGreaterThanOrEqual(1),
    );
    // 无常驻横幅:披露不出现在页面常态
    expect(screen.queryByText(/明文保存/)).toBeNull();
    // 打开录入对话框,披露以用户事实措辞呈现
    fireEvent.click(screen.getAllByText("录入凭据")[0]);
    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByText(/明文保存在本地数据库与备份档/)).toBeTruthy();
  });

  it("auth_failed 展示「需重新授权」文案,expired 展示过期推导;双形态各两份", async () => {
    renderPage({
      accounts: [
        account({ id: "a1", status: "auth_failed", status_reason: "刷新被拒(401)" }),
        account({ id: "a2", status: "expired" }),
      ],
      meta: { flow_configured: true },
    });
    await vi.waitFor(() =>
      // 桌面表格与移动卡片双形态同渲染:锁 ===2,任一形态渲染丢失即红
      expect(screen.getAllByText("授权失效(需重新授权)").length).toBe(2),
    );
    expect(screen.getAllByText("凭据已过期(待刷新)").length).toBe(2);
    // 原因文案在徽章文本外还有 title 副本(双形态共 4 份),锁下限防任一形态丢失
    expect(screen.getAllByText("刷新被拒(401)").length).toBeGreaterThanOrEqual(2);
    // 凭据原文不回显,仅脱敏形态;卡片形态对部分列有双渲染副本,锁下限防任一形态丢失
    expect(screen.getAllByText("sk-sec…(26 字符)").length).toBeGreaterThanOrEqual(2);
  });

  it("flow_configured=false 时行级授权按钮隐藏,页头发起授权同隐", async () => {
    renderPage({
      accounts: [account({ id: "acc-9" })],
      meta: { flow_configured: false },
    });
    await vi.waitFor(() => expect(screen.getAllByLabelText("删除 acc-9").length).toBe(2));
    expect(screen.queryByLabelText("重新授权 acc-9")).toBeNull();
    expect(screen.queryByText("发起授权")).toBeNull();
  });

  it("flow_configured=true 时授权按钮可见,点击调授权接口", async () => {
    vi.mocked(api.oauthAuthorize).mockResolvedValue({
      authorize_url: "https://sso.example/authorize?x=1",
    });
    renderPage({
      accounts: [account({ id: "acc-9" })],
      meta: { flow_configured: true },
    });
    await vi.waitFor(() => expect(screen.getAllByLabelText("重新授权 acc-9").length).toBe(2));
    fireEvent.click(screen.getAllByLabelText("重新授权 acc-9")[0]);
    await vi.waitFor(() => expect(vi.mocked(api.oauthAuthorize)).toHaveBeenCalledWith("acc-9"));
  });

  it("删除走确认弹窗,确认后调用删除接口", async () => {
    vi.mocked(api.oauthAccountDelete).mockResolvedValue({ ok: true });
    renderPage({
      accounts: [account({ id: "acc-9" })],
      meta: { flow_configured: false },
    });
    await vi.waitFor(() => expect(screen.getAllByLabelText("删除 acc-9").length).toBe(2));
    fireEvent.click(screen.getAllByLabelText("删除 acc-9")[0]);
    // 确认弹窗出现(与行内删除按钮同文案,经 dialog 角色域定位),先取消不删
    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByText(/删除订阅账号 — acc-9/)).toBeTruthy();
    fireEvent.click(within(dialog).getByText("取消"));
    expect(vi.mocked(api.oauthAccountDelete)).not.toHaveBeenCalled();
    // 再开并确认
    fireEvent.click(screen.getAllByLabelText("删除 acc-9")[0]);
    fireEvent.click(within(screen.getByRole("alertdialog")).getByText("删除"));
    await vi.waitFor(() => expect(vi.mocked(api.oauthAccountDelete)).toHaveBeenCalledWith("acc-9"));
  });
});
