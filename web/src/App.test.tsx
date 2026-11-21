import { fireEvent, render, screen, waitFor, cleanup } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import App from "./App";

vi.mock("./pages/Overview", () => ({ default: () => <div>总览内容</div> }));
const mocks = vi.hoisted(() => ({
  whoami: vi.fn(),
  logoutSso: vi.fn(),
  federationInfo: vi.fn(),
  setToken: vi.fn(),
}));
vi.mock("./lib/api", async () => {
  const actual = await vi.importActual<typeof import("./lib/api")>("./lib/api");
  return {
    ...actual,
    api: {
      whoami: mocks.whoami,
      logoutSso: mocks.logoutSso,
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
  mocks.logoutSso.mockResolvedValue({ redirect_url: "/console/" });
  mocks.federationInfo.mockResolvedValue({ enabled: false });
});
afterEach(cleanup);

describe("统一身份登录入口", () => {
  it("注销 401 回到登录入口", async () => {
    const { ApiError } = await import("./lib/api");
    mocks.whoami.mockResolvedValue({ kind: "federated", name: "张三" });
    mocks.logoutSso.mockRejectedValue(new ApiError(401, "登录已过期", "401"));
    render(<App />);
    await screen.findByText("总览内容");
    fireEvent.click(screen.getByRole("button", { name: "退出" }));
    await screen.findByRole("heading", { name: "木牛控制台" });
    expect(screen.queryByRole("alert")).toBeNull();
  });
  it("重载从 whoami 恢复会话，退出先撤销服务端会话", async () => {
    mocks.whoami.mockResolvedValue({ kind: "federated", name: "张三" });
    render(<App />);
    await screen.findByText("总览内容");
    fireEvent.click(screen.getByRole("button", { name: "退出" }));
    await screen.findByRole("heading", { name: "木牛控制台" });
    expect(mocks.logoutSso).toHaveBeenCalledOnce();
  });
  it("不再渲染本地账密表单", async () => {
    const { ApiError } = await import("./lib/api");
    mocks.whoami.mockRejectedValue(new ApiError(401, "登录已过期", "401"));
    mocks.federationInfo.mockResolvedValue({ enabled: true });
    render(<App />);
    await screen.findByRole("button", { name: "统一身份登录" });
    expect(screen.queryByLabelText("用户名")).toBeNull();
    expect(screen.queryByLabelText("密码")).toBeNull();
  });
  it("过期会话回登录，基础设施错误显示重试", async () => {
    mocks.whoami.mockResolvedValue({ kind: "federated", name: "张三" });
    render(<App />);
    await screen.findByText("总览内容");
    window.dispatchEvent(new Event("muniu-session-expired"));
    await screen.findByRole("heading", { name: "木牛控制台" });
    cleanup();
    mocks.whoami.mockRejectedValue(new Error("网络连接失败"));
    render(<App />);
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("网络连接失败"));
    expect(screen.getByRole("button", { name: "重试" })).toBeTruthy();
  });
});

describe("管理令牌登录", () => {
  it("令牌登录成功进入，失败可见且不进入", async () => {
    const { ApiError } = await import("./lib/api");
    mocks.whoami.mockRejectedValueOnce(new ApiError(401, "登录已过期", "401"));
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "使用管理令牌登录" }));
    fireEvent.change(screen.getByLabelText("管理员令牌"), { target: { value: "ops-token" } });
    mocks.whoami.mockResolvedValueOnce({ kind: "token", name: "management-token" });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    await screen.findByText("总览内容");
    expect(mocks.setToken).toHaveBeenCalledWith("ops-token");
  });
});

describe("虎符统一身份登录", () => {
  it("入口可用时出现，回调失败提示回显并清除查询参数", async () => {
    const { ApiError } = await import("./lib/api");
    mocks.whoami.mockRejectedValue(new ApiError(401, "登录已过期", "401"));
    mocks.federationInfo.mockResolvedValue({ enabled: true });
    window.history.replaceState(
      null,
      "",
      "/console/?oidc_error=" + encodeURIComponent("统一身份登录校验失败"),
    );
    render(<App />);
    await screen.findByRole("button", { name: "统一身份登录" });
    expect(screen.getByRole("alert").textContent).toContain("统一身份登录校验失败");
    expect(window.location.search).toBe("");
    cleanup();
  });
  it("点击统一身份登录前清除残留令牌，避免跨轨身份回落", async () => {
    const { ApiError } = await import("./lib/api");
    mocks.whoami.mockRejectedValue(new ApiError(401, "登录已过期", "401"));
    mocks.federationInfo.mockResolvedValue({ enabled: true });
    render(<App />);
    const sso = await screen.findByRole<HTMLButtonElement>("button", { name: "统一身份登录" });
    await waitFor(() => expect(sso.disabled).toBe(false));
    fireEvent.click(sso);
    expect(mocks.setToken).toHaveBeenCalledWith("");
  });
  it("联邦未配置时入口禁用并提示，仍可用管理令牌", async () => {
    const { ApiError } = await import("./lib/api");
    mocks.whoami.mockRejectedValue(new ApiError(401, "登录已过期", "401"));
    render(<App />);
    const sso = await screen.findByRole<HTMLButtonElement>("button", { name: "统一身份登录" });
    expect(sso.disabled).toBe(true);
    expect(screen.getByText("统一身份登录未配置,请联系平台管理员")).toBeTruthy();
    expect(screen.getByRole("button", { name: "使用管理令牌登录" })).toBeTruthy();
  });
  it("联邦会话经 whoami 恢复并展示实名", async () => {
    mocks.whoami.mockResolvedValue({ kind: "federated", name: "张三" });
    render(<App />);
    await screen.findByText("总览内容");
    expect(screen.getByText("张三")).toBeTruthy();
    expect(screen.queryByText("共享管理令牌")).toBeNull();
  });
});
