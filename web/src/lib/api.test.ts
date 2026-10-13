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
    const old = api.listTenants().catch((e) => e);
    setToken("");
    await api.login("alice", "password123");
    finish(new Response('{"error":{"message":"凭据已过期"}}', { status: 401 }));
    await old;
    expect(expired).not.toHaveBeenCalled();
  } finally {
    window.removeEventListener("muniu-session-expired", expired);
  }
});
