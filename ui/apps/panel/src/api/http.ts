export type HttpError = Error & { status: number; code: string };
export type KubeStatusError = HttpError & { reason: string; causes: { field: string; message: string }[] };

export function httpError(status: number, code: string, message?: string): HttpError {
  return Object.assign(new Error(message ?? code), { status, code });
}

interface StatusBody {
  kind?: string;
  reason?: string;
  message?: string;
  details?: { causes?: { field?: string; message?: string }[] };
}

export function kubeStatusError(status: number, body: StatusBody): KubeStatusError {
  const reason = body.reason ?? `Http${status}`;
  const causes = (body.details?.causes ?? []).map((cause) => ({ field: cause.field ?? "", message: cause.message ?? "" }));
  return Object.assign(httpError(status, reason.toLowerCase(), body.message ?? reason), { reason, causes });
}

export function isHttpError(value: unknown): value is HttpError {
  return value instanceof Error && typeof (value as Partial<HttpError>).status === "number" && typeof (value as Partial<HttpError>).code === "string";
}

export function isKubeStatusError(value: unknown): value is KubeStatusError {
  return isHttpError(value) && typeof (value as Partial<KubeStatusError>).reason === "string" && Array.isArray((value as Partial<KubeStatusError>).causes);
}

export interface RequestOptions {
  body?: unknown;
  contentType?: string;
  signal?: AbortSignal;
}

export interface Http {
  request<T>(method: string, path: string, options?: RequestOptions): Promise<T>;
  resetUnauthenticated(): void;
}

export interface HttpDeps {
  fetch: typeof fetch;
  csrf: () => string | undefined;
  onUnauthenticated: () => void;
}

const unsafeMethods: ReadonlySet<string> = new Set(["POST", "PUT", "PATCH", "DELETE"]);

function parseJson(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

function errorFrom(status: number, text: string): HttpError {
  const body = parseJson(text);
  if (body && typeof body === "object") {
    const record = body as StatusBody & { error?: string };
    if (record.kind === "Status") {
      return kubeStatusError(status, record);
    }
    if (typeof record.error === "string") {
      return httpError(status, record.error);
    }
  }
  return httpError(status, `http_${status}`);
}

export function createHttp(deps: HttpDeps): Http {
  let unauthenticatedReported = false;

  async function request<T>(method: string, path: string, options: RequestOptions = {}): Promise<T> {
    const headers: Record<string, string> = { Accept: "application/json" };
    if (unsafeMethods.has(method)) {
      const token = deps.csrf();
      if (!token) {
        throw httpError(0, "csrf_missing");
      }
      headers["X-CSRF-Token"] = token;
    }
    if (options.body !== undefined) {
      headers["Content-Type"] = options.contentType ?? "application/json";
    }
    const init: RequestInit = { method, headers, credentials: "same-origin" };
    if (options.body !== undefined) {
      init.body = JSON.stringify(options.body);
    }
    if (options.signal) {
      init.signal = options.signal;
    }
    const response = await deps.fetch(path, init).catch(() => {
      throw httpError(0, "network");
    });
    const text = await response.text();
    if (response.status === 401 && !unauthenticatedReported) {
      unauthenticatedReported = true;
      deps.onUnauthenticated();
    }
    if (!response.ok) {
      throw errorFrom(response.status, text);
    }
    return (text === "" ? undefined : parseJson(text)) as T;
  }

  return {
    request,
    resetUnauthenticated: () => {
      unauthenticatedReported = false;
    },
  };
}
