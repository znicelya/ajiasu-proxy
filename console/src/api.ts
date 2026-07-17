export type Page<T> = { items: T[]; next_cursor?: string };

export type ResourceKind =
  | "tenants"
  | "members"
  | "accounts"
  | "pools"
  | "endpoints"
  | "operations"
  | "nodes"
  | "health"
  | "quotas"
  | "audit";

export type ResourceRecord = Record<string, unknown> & { id?: string; name?: string; state?: string; health?: string };

export class ApiError extends Error {
  constructor(public readonly status: number, public readonly code: string, message: string) {
    super(message);
    this.name = "ApiError";
  }
}

function csrfToken(): string | undefined {
  return document.cookie.split(";").map((part) => part.trim()).find((part) => part.startsWith("XSRF-TOKEN="))?.split("=")[1];
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  const csrf = csrfToken();
  if (csrf) headers.set("X-CSRF-Token", decodeURIComponent(csrf));
  const response = await fetch(`/api/v1${path}`, { ...init, credentials: "include", headers });
  if (response.status === 401) throw new ApiError(401, "reauthentication_required", "Your session has expired.");
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as { code?: string; message?: string };
    throw new ApiError(response.status, body.code ?? "request_failed", body.message ?? "The request could not be completed.");
  }
  if (response.status === 204) return undefined as T;
  return (await response.json()) as T;
}

export function listResource(tenantId: string, kind: ResourceKind, cursor?: string): Promise<Page<ResourceRecord>> {
  const query = cursor ? `?limit=50&after=${encodeURIComponent(cursor)}` : "?limit=50";
  if (kind === "health") return fetch("/readyz", { credentials: "include" }).then(async (response) => ({ items: [{ id: "control-plane", state: response.ok ? "ready" : "not_ready", status: response.status }] }));
  if (kind === "quotas") return request<ResourceRecord>(`/tenants/${encodeURIComponent(tenantId)}/quota`).then((item) => ({ items: [item] }));
  const path = kind === "audit" ? `/audit-events?tenant_id=${encodeURIComponent(tenantId)}&limit=50` : kind === "nodes" ? `/nodes${query}` : kind === "tenants" ? `/tenants${query}` : `/tenants/${encodeURIComponent(tenantId)}/${resourcePath(kind)}${query}`;
  return request<Page<ResourceRecord>>(path);
}

function resourcePath(kind: ResourceKind): string {
  return ({ tenants: "tenants", members: "members", accounts: "accounts", pools: "account-pools", endpoints: "endpoints", operations: "operations", nodes: "nodes", health: "health", quotas: "quota", audit: "audit-events" } as const)[kind];
}

export function getOverview(tenantId: string): Promise<Record<string, unknown>> {
  return Promise.all([
    listResource(tenantId, "accounts"),
    listResource(tenantId, "pools"),
    listResource(tenantId, "endpoints")
  ]).then(([accounts, pools, endpoints]) => ({
    accounts: accounts.items.length,
    pools: pools.items.length,
    endpoints: endpoints.items.length,
    nodes: "—",
    gateways: "—",
    fixed_assigned: endpoints.items.filter((item) => item.state === "assigned").length,
    pool_assigned: pools.items.filter((item) => item.state === "assigned").length
  }));
}

export function updateResource(tenantId: string, kind: ResourceKind, id: string, body: unknown, version: number): Promise<ResourceRecord> {
  const prefix = kind === "nodes" ? "/nodes" : kind === "tenants" ? "/tenants" : `/tenants/${encodeURIComponent(tenantId)}/${resourcePath(kind)}`;
  return request<ResourceRecord>(`${prefix}/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: { "If-Match": `\"${version}\"`, "Idempotency-Key": crypto.randomUUID() },
    body: JSON.stringify(body)
  });
}
