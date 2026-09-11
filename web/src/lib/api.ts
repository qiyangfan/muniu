// 管理 API 客户端:类型一律来自 openapi-typescript 生成的 api-types.d.ts(禁手写 API 类型,ADR-005 纪律)。
import type { components, paths } from "./api-types";

export type Tenant = components["schemas"]["Tenant"];
export type KeyRecord = components["schemas"]["KeyRecord"];
export type UsagePoint = components["schemas"]["UsagePoint"];
export type ProviderInfo = components["schemas"]["ProviderInfo"];
export type CacheStats = components["schemas"]["CacheStats"];

const TOKEN_KEY = "muniu-admin-token";

export function getToken(): string {
  return sessionStorage.getItem(TOKEN_KEY) ?? "";
}
export function setToken(t: string): void {
  sessionStorage.setItem(TOKEN_KEY, t);
}

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const resp = await fetch(path, {
    method,
    headers: {
      Authorization: `Bearer ${getToken()}`,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await resp.text();
  if (!resp.ok) {
    let msg = `HTTP ${resp.status}`;
    try {
      const env = JSON.parse(text) as { error?: { message?: string } };
      if (env.error?.message) msg = env.error.message;
    } catch {
      /* 非 JSON 错误体,保留状态码信息 */
    }
    throw new ApiError(resp.status, msg);
  }
  return text ? (JSON.parse(text) as T) : (undefined as T);
}

export const api = {
  listTenants: () => call<{ tenants: Tenant[] }>("GET", "/admin/v1/tenants"),
  createTenant: (name: string) => call<{ id: string }>("POST", "/admin/v1/tenants", { name }),
  patchTenant: (id: string, patch: Partial<Pick<Tenant, "status">> & { daily_quota_micro?: number; tpm_limit?: number; semantic_cache_enabled?: boolean }) =>
    call<{ ok: boolean }>("PATCH", `/admin/v1/tenants/${id}`, patch),
  listKeys: () => call<{ keys: KeyRecord[] }>("GET", "/admin/v1/keys"),
  createKey: (tenant: string, name: string, rpm_limit: number) =>
    call<{ key: string; prefix: string }>("POST", "/admin/v1/keys", { tenant, name, rpm_limit }),
  revokeKey: (prefix: string) => call<{ revoked: number }>("DELETE", `/admin/v1/keys/${prefix}`),
  usage: (q: { group_by: string; from?: string; to?: string; tenant_id?: string; key_id?: string }) => {
    const qs = new URLSearchParams(Object.entries(q).filter(([, v]) => v) as [string, string][]);
    return call<{ points: UsagePoint[] }>("GET", `/admin/v1/usage?${qs}`);
  },
  providers: () => call<{ providers: ProviderInfo[] }>("GET", "/admin/v1/providers"),
  cacheStats: () => call<CacheStats>("GET", "/admin/v1/cache/stats"),
};

export type _Paths = paths; // 保留生成契约引用
