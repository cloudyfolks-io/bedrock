import { vi } from "vitest";
import type { Check } from "../api/types";

export interface FakeRequest {
  method: string;
  url: string;
  path: string;
  query: URLSearchParams;
  headers: Headers;
  body: unknown;
}

export interface FakeReply {
  status?: number;
  body?: unknown;
  headers?: Record<string, string>;
}

export type FakeRoute = FakeReply | ((request: FakeRequest) => FakeReply | Promise<FakeReply>);

export interface FakeApi {
  calls: FakeRequest[];
  fetch: typeof fetch;
  install: () => void;
  restore: () => void;
}

function matches(pattern: string, method: string, path: string): boolean {
  const [patternMethod, patternPath] = pattern.split(" ");
  if (patternMethod !== method || !patternPath) {
    return false;
  }
  return patternPath.endsWith("*") ? path.startsWith(patternPath.slice(0, -1)) : patternPath === path;
}

function parseBody(init: RequestInit | undefined): unknown {
  if (typeof init?.body !== "string") {
    return undefined;
  }
  try {
    return JSON.parse(init.body);
  } catch {
    return init.body;
  }
}

export function createFakeApi(routes: Record<string, FakeRoute>, allow?: (check: Check) => boolean): FakeApi {
  const calls: FakeRequest[] = [];

  async function fake(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    const url = new URL(String(input), "https://console.test");
    const request: FakeRequest = {
      method: (init?.method ?? "GET").toUpperCase(),
      url: url.pathname + url.search,
      path: url.pathname,
      query: url.searchParams,
      headers: new Headers(init?.headers),
      body: parseBody(init),
    };
    calls.push(request);
    const key = Object.keys(routes).find((pattern) => matches(pattern, request.method, request.path));
    if (!key && request.method === "POST" && request.path === "/proxy/v1/authz" && allow) {
      const checks = (request.body as { checks: Check[] }).checks;
      return reply({ body: { results: checks.map((check) => ({ allowed: allow(check) })) } });
    }
    if (!key) {
      return reply({ status: 404, body: { error: "not_found" } });
    }
    const route = routes[key];
    return reply(typeof route === "function" ? await route(request) : (route ?? {}));
  }

  function reply(value: FakeReply): Response {
    const status = value.status ?? 200;
    const text = value.body === undefined ? null : JSON.stringify(value.body);
    return new Response(status === 204 ? null : text, { status, headers: value.headers ?? { "Content-Type": "application/json" } });
  }

  return {
    calls,
    fetch: fake as typeof fetch,
    install: () => {
      vi.stubGlobal("fetch", fake);
    },
    restore: () => {
      vi.unstubAllGlobals();
    },
  };
}
