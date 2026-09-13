// 管理 API 客户端:类型一律来自 openapi-typescript 生成的 api-types.d.ts(禁手写 API 类型,ADR-005 纪律)。
import type { components, paths } from "./api-types";

export type Tenant = components["schemas"]["Tenant"];
export type KeyRecord = components["schemas"]["KeyRecord"];
export type UsagePoint = components["schemas"]["UsagePoint"];
export type ProviderInfo = components["schemas"]["ProviderInfo"];
export type CacheStats = components["schemas"]["CacheStats"];
export type PricingEntry = components["schemas"]["PricingView"];
export type PricingWindow = components["schemas"]["PricingWindow"];

const TOKEN_KEY = "muniu-admin-token";

export function getToken(): string {
  return sessionStorage.getItem(TOKEN_KEY) ?? "";
}
export function setToken(t: string): void {
  sessionStorage.setItem(TOKEN_KEY, t);
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    /** 后端原始错误文本(未转译),排障用;展示层一律读 message */
    public raw: string,
  ) {
    super(message);
  }
}

// HTTP 状态码 → 中文兜底文案。仅在响应体没有可读中文 detail 时使用
// (网关 502/超时等非 JSON 响应、后端英文 detail 都会落到这里)
const HTTP_MESSAGES: Record<number, string> = {
  400: "请求有误,请检查后重试",
  401: "登录已过期,请重新登录",
  403: "没有执行此操作的权限",
  404: "目标不存在或已被删除",
  405: "请求方式不受支持",
  422: "提交的内容未通过校验,请检查后重试",
  429: "操作过于频繁,请稍后再试",
  500: "服务开小差了,请稍后重试",
  502: "服务暂时不可用,请稍后重试",
  503: "服务维护中,请稍后重试",
  504: "服务响应超时,请稍后重试",
};

// detail 含 CJK 即视为后端面向管理员的业务错误文案(管理面错误统一中文),原样展示
const CJK_RE = /[\u3400-\u4dbf\u4e00-\u9fff]/;

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  let resp: Response;
  try {
    resp = await fetch(path, {
      method,
      headers: {
        Authorization: `Bearer ${getToken()}`,
        ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch (err) {
    // fetch reject 的 TypeError 文案是浏览器英文("Failed to fetch" 等),不示人
    const e = new Error("网络连接失败,请检查网络后重试") as Error & { raw?: string };
    e.raw = err instanceof Error ? err.message : String(err);
    throw e;
  }
  const text = await resp.text();
  if (!resp.ok) {
    let detail = "";
    try {
      const env = JSON.parse(text) as { error?: { message?: string } };
      if (env.error?.message) detail = env.error.message;
    } catch {
      /* 非 JSON 错误体(网关页/空体),走状态码兜底 */
    }
    const msg = CJK_RE.test(detail)
      ? detail
      : (HTTP_MESSAGES[resp.status] ?? `请求失败(HTTP ${resp.status})`);
    throw new ApiError(resp.status, msg, detail || `HTTP ${resp.status}`);
  }
  return text ? (JSON.parse(text) as T) : (undefined as T);
}

export const api = {
  listTenants: () => call<{ tenants: Tenant[] }>("GET", "/admin/v1/tenants"),
  createTenant: (name: string) => call<{ id: string }>("POST", "/admin/v1/tenants", { name }),
  patchTenant: (
    id: string,
    patch: Partial<Pick<Tenant, "status">> & {
      daily_quota_credits?: number;
      tpm_limit?: number;
      semantic_cache_enabled?: boolean;
    },
  ) => call<{ ok: boolean }>("PATCH", `/admin/v1/tenants/${id}`, patch),
  listKeys: () => call<{ keys: KeyRecord[] }>("GET", "/admin/v1/keys"),
  createKey: (
    tenant: string,
    name: string,
    rpm_limit: number,
    opts?: { tpm_limit?: number; daily_quota_credits?: number; expires_at?: string },
  ) =>
    call<{ key: string; prefix: string }>("POST", "/admin/v1/keys", {
      tenant,
      name,
      rpm_limit,
      ...opts,
    }),
  patchKey: (
    id: string,
    patch: {
      rpm_limit?: number;
      tpm_limit?: number;
      daily_quota_credits?: number;
      expires_at?: string | null;
    },
  ) => call<KeyRecord>("PATCH", `/admin/v1/keys/${id}`, patch),
  revokeKey: (prefix: string) => call<{ revoked: number }>("DELETE", `/admin/v1/keys/${prefix}`),
  usage: (q: {
    group_by: string;
    from?: string;
    to?: string;
    tenant_id?: string;
    key_id?: string;
  }) => {
    const qs = new URLSearchParams(Object.entries(q).filter(([, v]) => v) as [string, string][]);
    return call<{ points: UsagePoint[] }>("GET", `/admin/v1/usage?${qs}`);
  },
  providers: () => call<{ providers: ProviderInfo[] }>("GET", "/admin/v1/providers"),
  cacheStats: () => call<CacheStats>("GET", "/admin/v1/cache/stats"),
  listPricing: () => call<{ pricing: PricingEntry[] }>("GET", "/admin/v1/pricing"),
  putPricing: (alias: string, body: PricingEntry) =>
    call<{ ok: boolean }>("PUT", `/admin/v1/pricing/${encodeURIComponent(alias)}`, body),
  deletePricing: (alias: string) =>
    call<{ ok: boolean }>("DELETE", `/admin/v1/pricing/${encodeURIComponent(alias)}`),
};

export type _Paths = paths; // 保留生成契约引用
