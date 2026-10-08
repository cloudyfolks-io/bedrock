import { describe, expect, it, vi } from "vitest";
import { createHttp, isHttpError, isKubeStatusError } from "./http";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), { status });
}

function setup(reply: () => Response | Promise<Response>, csrf = "tok") {
  const fetchMock = vi.fn((_input: RequestInfo | URL, _init?: RequestInit) => Promise.resolve(reply()));
  const onUnauthenticated = vi.fn();
  const http = createHttp({ fetch: fetchMock as unknown as typeof fetch, csrf: () => csrf, onUnauthenticated });
  return { http, fetchMock, onUnauthenticated };
}

describe("createHttp", () => {
  it("TestRequestUsesSameOriginCredentials", async () => {
    const { http, fetchMock } = setup(() => jsonResponse(200, { ok: true }));
    await http.request("GET", "/apis/x");
    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(url).toBe("/apis/x");
    expect(init?.credentials).toBe("same-origin");
    expect((init?.headers as Record<string, string>).Accept).toBe("application/json");
  });

  it("TestCsrfHeaderOnlyOnUnsafeMethods", async () => {
    const { http, fetchMock } = setup(() => jsonResponse(200, {}));
    await http.request("GET", "/a");
    for (const method of ["POST", "PUT", "PATCH", "DELETE"]) {
      await http.request(method, "/a", { body: {} });
    }
    const headers = fetchMock.mock.calls.map((call) => call[1]?.headers as Record<string, string>);
    expect(headers[0]?.["X-CSRF-Token"]).toBeUndefined();
    for (const entry of headers.slice(1)) {
      expect(entry["X-CSRF-Token"]).toBe("tok");
    }
  });

  it("rejects an unsafe request without a token before any request", async () => {
    const { http, fetchMock } = setup(() => jsonResponse(200, {}), "");
    await expect(http.request("POST", "/a", { body: {} })).rejects.toMatchObject({ status: 0, code: "csrf_missing" });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("TestKubeStatusErrorsAreParsed", async () => {
    const invalid = setup(() =>
      jsonResponse(422, { kind: "Status", reason: "Invalid", message: "bad", details: { causes: [{ field: "spec.cidr", message: "bad" }] } }),
    );
    const error = await invalid.http.request("PUT", "/a", { body: {} }).catch((reason: unknown) => reason);
    expect(isKubeStatusError(error)).toBe(true);
    expect(isKubeStatusError(error) && error.causes).toEqual([{ field: "spec.cidr", message: "bad" }]);
    const conflict = setup(() => jsonResponse(409, { kind: "Status", reason: "Conflict", message: "changed" }));
    const conflictError = await conflict.http.request("PUT", "/a", { body: {} }).catch((reason: unknown) => reason);
    expect(isKubeStatusError(conflictError) && conflictError.reason).toBe("Conflict");
    const plain = setup(() => jsonResponse(403, { error: "forbidden" }));
    const plainError = await plain.http.request("GET", "/a").catch((reason: unknown) => reason);
    expect(isHttpError(plainError) && plainError.code).toBe("forbidden");
    expect(isKubeStatusError(plainError)).toBe(false);
  });

  it("TestUnauthenticatedCallsTheHookOnce", async () => {
    const { http, onUnauthenticated } = setup(() => jsonResponse(401, { error: "unauthenticated" }));
    await Promise.allSettled([http.request("GET", "/a"), http.request("GET", "/b"), http.request("GET", "/c")]);
    expect(onUnauthenticated).toHaveBeenCalledTimes(1);
  });

  it("TestPatchUsesMergePatchContentType", async () => {
    const { http, fetchMock } = setup(() => jsonResponse(200, {}));
    await http.request("PATCH", "/a", { body: { spec: {} }, contentType: "application/merge-patch+json" });
    const headers = fetchMock.mock.calls[0]?.[1]?.headers as Record<string, string>;
    expect(headers["Content-Type"]).toBe("application/merge-patch+json");
  });

  it("TestNoTokenInUrls", async () => {
    const { http, fetchMock } = setup(() => jsonResponse(200, {}));
    await http.request("POST", "/apis/x/upload", { body: {} });
    for (const call of fetchMock.mock.calls) {
      expect(String(call[0])).not.toMatch(/token|tok/);
    }
  });

  it("TestNetworkFailureIsAnHttpError", async () => {
    const fetchMock = vi.fn(() => Promise.reject(new TypeError("offline")));
    const http = createHttp({ fetch: fetchMock as unknown as typeof fetch, csrf: () => "t", onUnauthenticated: () => {} });
    await expect(http.request("GET", "/a")).rejects.toMatchObject({ status: 0, code: "network" });
  });

  it("answers undefined for an empty body", async () => {
    const { http } = setup(() => new Response(null, { status: 204 }));
    await expect(http.request("DELETE", "/a")).resolves.toBeUndefined();
  });
});
