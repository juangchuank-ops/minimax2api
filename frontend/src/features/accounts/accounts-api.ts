import { apiRequest } from "@/shared/api/client";

export type AccountStatus = "active" | "cooldown" | "disabled" | "invalid";
export type AccountKind = "token" | "guest";

/**
 * Region picks which MiniMax deployment the account belongs to. The two run
 * separate account databases, so a token from one is rejected by the other.
 */
export type AccountRegion = "cn" | "global";

/**
 * "auto" is a request-level sentinel, never a stored value: it tells the server
 * to infer the region from the token's own claims. It exists because a token
 * with no phone number carries no hint, and guessing wrong sends every request
 * to a host that rejects it.
 */
export type AccountRegionInput = AccountRegion | "auto";

export type AccountQuota = {
  syncedAt: string;
  available: boolean;
  latencyMs: number;
  plan: string;
  note: string;
};

/**
 * Outcome of the last daily check-in attempt. "" means it has never been tried,
 * which is a distinct state from "failed" and worth showing differently.
 *
 * "unpaid" is the one the upstream cannot report itself: the claim endpoint
 * answers success whether or not the points were issued, so it is decided later
 * from the credit grants.
 */
export type AccountSigninStatus = "" | "ok" | "already" | "failed" | "skipped" | "unpaid";

/** One slot of the seven-day check-in cycle. */
export type SigninDay = {
  dayNo: number;
  points: number;
  status: number;
  isToday: boolean;
};

export type AccountSigninPanel = {
  scene: number;
  days: SigninDay[];
};

/**
 * Balance reported by the upstream.
 *
 * `total` is the number routing acts on. The upstream reports it as a string
 * inside op_credit_summary; the backend parses it, so it arrives here as a
 * number. `free` and `purchased` split that total by origin.
 */
export type AccountCredit = {
  total: number;
  free: number;
  purchased: number;
  planName: string;
  planType: number;
  syncedAt: string;
};

export type AccountDTO = {
  id: string;
  name: string;
  kind: AccountKind;
  region: AccountRegion;
  userId: string;
  identifier: string;
  agentID: string;
  deviceID: string;
  uuid: string;
  screenWidth: number;
  screenHeight: number;
  baseURL: string;
  group: string;
  remark: string;
  status: AccountStatus;
  enabled: boolean;
  priority: number;
  maxConcurrent: number;
  inflight: number;
  cooldownUntil: string;
  failCount: number;
  successCount: number;
  lastUsedAt: string;
  lastError: string;
  createdAt: string;
  updatedAt: string;
  tokenMasked: string;
  quota: AccountQuota | null;
  signinAt: string;
  signinStatus: AccountSigninStatus;
  signinStreak: number;
  signinPoints: number;
  signinTotal: number;
  signinError: string;
  signinPanel: AccountSigninPanel | null;
  credit: AccountCredit | null;
};

export type AccountSummary = {
  total: number;
  active: number;
  cooldown: number;
  disabled: number;
  invalid: number;
  routable: number;
};

export type AccountListResult = {
  items: AccountDTO[];
  total: number;
  page: number;
  pageSize: number;
  summary: AccountSummary;
};

export type AccountQuery = {
  page: number;
  pageSize: number;
  search?: string;
  status?: string;
  kind?: string;
  group?: string;
  sortBy?: string;
  sortOrder?: "asc" | "desc";
};

/** Fields the console may write. The backend derives whatever is omitted. */
export type AccountInput = {
  name?: string;
  token?: string;
  region?: AccountRegionInput;
  userId?: string;
  agentID?: string;
  deviceID?: string;
  uuid?: string;
  screenWidth?: number;
  screenHeight?: number;
  baseURL?: string;
  group?: string;
  remark?: string;
  priority?: number;
  maxConcurrent?: number;
  enabled?: boolean;
};

export function listAccounts(query: AccountQuery): Promise<AccountListResult> {
  const params = new URLSearchParams();
  params.set("page", String(query.page));
  params.set("pageSize", String(query.pageSize));
  if (query.search) params.set("search", query.search);
  if (query.status) params.set("status", query.status);
  if (query.kind) params.set("kind", query.kind);
  if (query.group) params.set("group", query.group);
  if (query.sortBy) params.set("sortBy", query.sortBy);
  if (query.sortOrder) params.set("sortOrder", query.sortOrder);
  return apiRequest<AccountListResult>(`/admin/api/accounts?${params.toString()}`);
}

export function createAccount(payload: AccountInput): Promise<{ account: AccountDTO; quotaWarning?: string }> {
  return apiRequest("/admin/api/accounts", { method: "POST", body: payload });
}

export function updateAccount(id: string, payload: AccountInput): Promise<{ account: AccountDTO }> {
  return apiRequest(`/admin/api/accounts/${id}`, { method: "PATCH", body: payload });
}

export function deleteAccount(id: string): Promise<void> {
  return apiRequest(`/admin/api/accounts/${id}`, { method: "DELETE" });
}

export type BatchAction =
  | { action: "enable" | "disable" | "delete" | "clearCooldown" | "quota"; ids: string[] }
  | { action: "concurrency"; ids: string[]; maxConcurrent: number };

export function batchAccounts(payload: BatchAction): Promise<{
  updated?: number;
  deleted?: number;
  succeeded?: number;
  failed?: number;
}> {
  return apiRequest("/admin/api/accounts/batch", { method: "POST", body: payload });
}

/**
 * Bulk import. `tokens` takes one account per line in any of the shapes the
 * backend accepts, and `region` is the fallback for lines that do not name one.
 */
export function importAccounts(payload: {
  tokens?: string;
  region?: AccountRegionInput;
  json?: unknown;
}): Promise<{ created: number; updated: number; failed: number; errors?: string[] }> {
  return apiRequest("/admin/api/accounts/import", { method: "POST", body: payload });
}

export function exportAccounts(limit = 10000): Promise<{ accounts: unknown[]; count: number }> {
  return apiRequest(`/admin/api/accounts/export?limit=${limit}`);
}

export function probeAccount(id: string): Promise<{ ok: boolean; latencyMs: number; message: string; account: AccountDTO }> {
  return apiRequest(`/admin/api/accounts/${id}/probe`, { method: "POST" });
}

export function probeAllAccounts(): Promise<{ healthy: number; unhealthy: number }> {
  return apiRequest("/admin/api/accounts/probe-all", { method: "POST" });
}

export function refreshQuota(id: string): Promise<{ account: AccountDTO }> {
  return apiRequest(`/admin/api/accounts/${id}/quota`, { method: "POST" });
}

export function refreshAllQuota(): Promise<{ succeeded: number; failed: number }> {
  return apiRequest("/admin/api/accounts/quota-all", { method: "POST" });
}

export function cleanupAccounts(statuses: string[]): Promise<{ deleted: number }> {
  return apiRequest("/admin/api/accounts/cleanup", { method: "POST", body: { statuses } });
}

export function listAccountGroups(): Promise<{ groups: string[] }> {
  return apiRequest("/admin/api/accounts/groups");
}

// ------------------------------------------------------------------- sign-in

export type SigninAccountResult = {
  id: string;
  name: string;
  status: AccountSigninStatus;
  points: number;
  streak: number;
  credit: number;
  error: string;
};

export type SigninReport = {
  startedAt: string;
  endedAt: string;
  total: number;
  claimed: number;
  already: number;
  failed: number;
  skipped: number;
  points: number;
  results: SigninAccountResult[];
};

export type SigninOverview = {
  enabled: boolean;
  running: boolean;
  nextRunAt: string;
  lastRunAt: string;
  lastReport: SigninReport | null;
  /** Whether a zero balance is actually holding accounts out of rotation. */
  skipZeroCredit: boolean;
  summary: {
    total: number;
    done: number;
    failed: number;
    skipped: number;
    exhausted: number;
    totalPoints: number;
  };
};

export function getSigninOverview(): Promise<SigninOverview> {
  return apiRequest("/admin/api/signin");
}

/**
 * Triggers a sweep now. Rejects with a 409 when one is already in flight, which
 * the caller should surface as "please wait" rather than as a failure.
 */
export function runSignin(): Promise<{ report: SigninReport; nextRunAt: string }> {
  return apiRequest("/admin/api/signin/run", { method: "POST" });
}

export function signinAccount(id: string): Promise<{ result: SigninAccountResult; account: AccountDTO }> {
  return apiRequest(`/admin/api/accounts/${id}/signin`, { method: "POST" });
}

export function refreshCredit(id: string): Promise<{ credit: AccountCredit; account: AccountDTO }> {
  return apiRequest(`/admin/api/accounts/${id}/credit`, { method: "POST" });
}
