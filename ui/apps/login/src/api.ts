import type { Account, Answer, Challenge, SessionInfo, TokenInfo } from "./types";

export class ApiError extends Error {
  readonly code: string;

  constructor(code: string) {
    super(code);
    this.code = code;
  }
}

async function readJSON<T>(response: Response): Promise<T> {
  if (response.status === 204) {
    return undefined as T;
  }
  const data = (await response.json()) as T & { error?: string };
  if (!response.ok) {
    throw new ApiError(typeof data.error === "string" ? data.error : "unknown");
  }
  return data;
}

function get<T>(path: string): Promise<T> {
  return fetch(path, { credentials: "same-origin" }).then((response) => readJSON<T>(response));
}

function post<T>(path: string, body?: unknown, csrf?: string): Promise<T> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (csrf) {
    headers["X-CSRF-Token"] = csrf;
  }
  const init: RequestInit = { method: "POST", credentials: "same-origin", headers };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
  }
  return fetch(path, init).then((response) => readJSON<T>(response));
}

function remove<T>(path: string, csrf: string): Promise<T> {
  return fetch(path, {
    method: "DELETE",
    credentials: "same-origin",
    headers: { "X-CSRF-Token": csrf },
  }).then((response) => readJSON<T>(response));
}

export function start(authRequest: string): Promise<Challenge> {
  return post<Challenge>("/api/v1/login/start", { authRequest });
}

export function startDevice(userCode: string): Promise<Challenge> {
  return post<Challenge>("/api/v1/login/device", { userCode });
}

export function challenge(): Promise<Challenge> {
  return get<Challenge>("/api/v1/login/challenge");
}

export function answer(body: Answer, csrf: string): Promise<Challenge> {
  return post<Challenge>("/api/v1/login/answer", body, csrf);
}

export function account(): Promise<Account> {
  return get<Account>("/api/v1/account");
}

export function changePassword(current: string, next: string, csrf: string): Promise<void> {
  return post<void>("/api/v1/account/password", { current, new: next }, csrf);
}

export function beginTOTP(csrf: string): Promise<{ otpauthURL: string; secret: string }> {
  return post("/api/v1/account/totp", undefined, csrf);
}

export function verifyTOTP(code: string, csrf: string): Promise<{ recoveryCodes: string[] }> {
  return post("/api/v1/account/totp/verify", { code }, csrf);
}

export function removeTOTP(csrf: string): Promise<void> {
  return remove<void>("/api/v1/account/totp", csrf);
}

export function newRecoveryCodes(csrf: string): Promise<{ recoveryCodes: string[] }> {
  return post("/api/v1/account/recovery-codes", undefined, csrf);
}

export function listTokens(): Promise<TokenInfo[]> {
  return get<TokenInfo[]>("/api/v1/account/tokens");
}

export function createToken(
  description: string,
  scopes: string[],
  expiresAt: string | null,
  csrf: string,
): Promise<{ id: string; token: string }> {
  return post("/api/v1/account/tokens", { description, scopes, expiresAt }, csrf);
}

export function revokeToken(id: string, csrf: string): Promise<void> {
  return remove<void>(`/api/v1/account/tokens/${id}`, csrf);
}

export function listSessions(): Promise<SessionInfo[]> {
  return get<SessionInfo[]>("/api/v1/account/sessions");
}

export function revokeSession(id: string, csrf: string): Promise<void> {
  return remove<void>(`/api/v1/account/sessions/${id}`, csrf);
}

export function logout(csrf: string): Promise<void> {
  return post<void>("/api/v1/account/logout", undefined, csrf);
}
