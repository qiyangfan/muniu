import { fireEvent, render, screen, waitFor, cleanup } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import App from "./App";

vi.mock("./pages/Overview", () => ({ default: () => <div>总览内容</div> }));
const mocks = vi.hoisted(() => ({
  whoami: vi.fn(),
  login: vi.fn(),
  logout: vi.fn(),
  federationInfo: vi.fn(),
  setToken: vi.fn(),
}));
vi.mock("./lib/api", async () => {
  const actual = await vi.importActual<typeof import("./lib/api")>("./lib/api");
  return {
    ...actual,
    api: {
      whoami: mocks.whoami,
      login: mocks.login,
      logout: mocks.logout,
      federationInfo: mocks.federationInfo,
    },
    setToken: mocks.setToken,
  };
});
beforeEach(() => {
  vi.clearAllMocks();
  window.scrollTo = vi.fn();
  window.matchMedia = vi
    .fn()
    .mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() });
  mocks.logout.mockResolvedValue({ ok: true });
  mocks.federationInfo.mockResolvedValue({ enabled: false });
});
afterEach(cleanup);

describe("管理员双轨登录", () => {
  it("注销 401 回到登录入口", async () => {
    const { ApiError } = await import("./lib/api");
    mocks.whoami.mockResolvedValue({ kind: "session", name: "alice" });
    mocks.logout.mockRejectedValue(new ApiError(401, "登录已过期", "401"));
    render(<App />);
    await screen.findByText("总览内容");
    fireEvent.click(screen.getByRole("button", { name: "退出" }));
    await screen.findByLabelText("用户名");
    expect(screen.queryByRole("alert")).toBeNull();
  });
  it("重载从 whoami 恢复会话，退出先撤销服务端会话", async () => {
    mocks.whoami.mockResolvedValue({ kind: "session", name: "alice" });
    render(<App />);
    await screen.findByText("总览内容");
    fireEvent.click(screen.getByRole("button", { name: "退出" }));
    await screen.findByLabelText("用户名");
    expect(mocks.logout).toHaveBeenCalledOnce();
  });
  it("账密成功进入，登录失败可见且不进入", async () => {
    const { ApiError } = await import("./lib/api");
    mocks.whoami.mockRejectedValue(new ApiError(401, "登录已过期", "401"));
    mocks.login
      .mockRejectedValueOnce(new ApiError(401, "账号暂时锁定", "locked"))
      .mockResolvedValueOnce({ kind: "session", name: "alice" });
    render(<App />);
    await screen.findByLabelText("用户名");
    fireEvent.change(screen.getByLabelText("用户名"), { target: { value: "alice" } });
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "password123" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.queryByText("总览内容")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    await screen.findByText("总览内容");
    expect(mocks.login).toHaveBeenCalledWith("alice", "password123");
  });
  it("过期会话回登录，基础设施错误显示重试", async () => {
    mocks.whoami.mockResolvedValue({ kind: "session", name: "alice" });
    render(<App />);
    await screen.findByText("总览内容");
    window.dispatchEvent(new Event("muniu-session-expired"));
    await screen.findByLabelText("用户名");
    cleanup();
    mocks.whoami.mockRejectedValue(new Error("网络连接失败"));
    render(<App />);
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("网络连接失败"));
    expect(screen.getByRole("button", { name: "重试" })).toBeTruthy();
  });
});

describe("锦官联邦登录", () => {
  it("联邦可用时出现锦官登录入口，回调失败提示回显并清除查询参数", async () => {
    const { ApiError } = await import("./lib/api");
    mocks.whoami.mockRejectedValue(new ApiError(401, "登录已过期", "401"));
    mocks.federationInfo.mockResolvedValue({ enabled: true });
    window.history.replaceState(
      null,
      "",
      "/console/?oidc_error=" + encodeURIComponent("锦官登录校验失败"),
    );
    render(<App />);
    await screen.findByRole("button", { name: "锦官登录" });
    expect(screen.getByRole("alert").textContent).toContain("锦官登录校验失败");
    expect(window.location.search).toBe("");
    cleanup();
  });
  it("点击锦官登录前清除残留令牌，避免跨轨身份回落", async () => {
    const { ApiError } = await import("./lib/api");
    mocks.whoami.mockRejectedValue(new ApiError(401, "登录已过期", "401"));
    mocks.federationInfo.mockResolvedValue({ enabled: true });
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "锦官登录" }));
    expect(mocks.setToken).toHaveBeenCalledWith("");
  });
  it("联邦不可用时不渲染入口，本地账密照常", async () => {
    const { ApiError } = await import("./lib/api");
    mocks.whoami.mockRejectedValue(new ApiError(401, "登录已过期", "401"));
    render(<App />);
    await screen.findByLabelText("用户名");
    expect(screen.queryByRole("button", { name: "锦官登录" })).toBeNull();
    expect(screen.getByRole("button", { name: "使用管理令牌登录" })).toBeTruthy();
  });
  it("联邦会话经 whoami 恢复并展示锦官实名", async () => {
    mocks.whoami.mockResolvedValue({ kind: "federated", name: "张三" });
    render(<App />);
    await screen.findByText("总览内容");
    expect(screen.getByText("张三")).toBeTruthy();
    expect(screen.queryByText("共享管理令牌")).toBeNull();
  });
});
