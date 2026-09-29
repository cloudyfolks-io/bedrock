import { afterEach, describe, expect, it, vi } from "vitest";
import * as api from "./api";
import fixtures from "./fixtures/challenges.json";
import type { Challenge } from "./types";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("challenge fixtures", () => {
  for (const fixture of fixtures as Array<{ name: string; challenge: Challenge }>) {
    it(`parses the ${fixture.name} challenge`, async () => {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(200, fixture.challenge)));
      await expect(api.challenge()).resolves.toEqual(fixture.challenge);
    });
  }
});

describe("start", () => {
  it("posts the authRequest id with no CSRF header", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { type: "username" }));
    vi.stubGlobal("fetch", fetchMock);
    await api.start("req-1");
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(init.body as string)).toEqual({ authRequest: "req-1" });
    expect((init.headers as Record<string, string>)["X-CSRF-Token"]).toBeUndefined();
  });
});

describe("answer", () => {
  it("sends the CSRF token", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { type: "done", redirect: "/x" }));
    vi.stubGlobal("fetch", fetchMock);
    await api.answer({ type: "password", password: "hunter2" }, "csrf-xyz");
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect((init.headers as Record<string, string>)["X-CSRF-Token"]).toBe("csrf-xyz");
  });

  it("maps a non-2xx JSON error to ApiError", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(423, { error: "locked" })));
    await expect(api.answer({ type: "password", password: "x" }, "csrf")).rejects.toMatchObject({ code: "locked" });
  });
});
