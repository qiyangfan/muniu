import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api, setToken } from "./api";

beforeEach(() => {
  sessionStorage.clear();
});
afterEach(() => {
  vi.unstubAllGlobals();
});
it("旧登录态的迟到401不能清除新登录态", async () => {
  let finish!: (value: Response) => void;
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            finish = resolve;
          }),
      )
      .mockResolvedValue(new Response('{"kind":"session","name":"alice"}', { status: 200 })),
  );
  const expired = vi.fn();
  window.addEventListener("muniu-session-expired", expired);
  try {
    setToken("old-token");
    const old = api.listTenants().catch((e: unknown) => e);
    setToken("new-token");
    await api.whoami();
    finish(new Response('{"error":{"message":"凭据已过期"}}', { status: 401 }));
    await old;
    expect(expired).not.toHaveBeenCalled();
  } finally {
    window.removeEventListener("muniu-session-expired", expired);
  }
});

it("运行时 API 路径与方法正确", async () => {
  setToken("tok");
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = input instanceof Request ? input.url : input.toString();
      calls.push({ url, init });
      return Promise.resolve(new Response("{}", { status: 200 }));
    }),
  );
  await api.runtimeStatus();
  await api.runtimeSharedControl();
  await api.runtimeCommands();
  await api.runtimeLogs(25);
  await api.runtimeExecuteCommand({ action: "cache_purge", params: { all: true }, reason: "r" });
  expect(calls.map((c) => c.url)).toEqual([
    "/admin/v1/runtime/status",
    "/admin/v1/runtime/shared-control",
    "/admin/v1/runtime/commands",
    "/admin/v1/runtime/logs?limit=25",
    "/admin/v1/runtime/commands",
  ]);
  expect(calls[4].init?.method).toBe("POST");
  expect(calls[4].init?.body).toBe(
    JSON.stringify({ action: "cache_purge", params: { all: true }, reason: "r" }),
  );
});
