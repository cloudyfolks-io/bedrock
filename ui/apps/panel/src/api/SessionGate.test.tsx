import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SessionGate, loginUrl, safeReturnTo, useSession } from "./session";
import { renderPanel } from "../test/renderPanel";
import type { Session } from "./types";

const session: Session = {
  user: { name: "admin", displayName: "Admin", email: "a@example.test", groups: ["bedrock-admins"] },
  admin: true,
  csrf: "csrf-token-for-tests-0123456789abcdefghijklm",
  expiresAt: "2026-10-09T00:00:00Z",
  host: "e2e.bedrock.test",
};

describe("SessionGate", () => {
  afterEach(() => {
    sessionStorage.clear();
    localStorage.clear();
  });

  it("TestGateShowsSpinnerThenChildren", async () => {
    renderPanel(
      <SessionGate>
        <p>inside</p>
      </SessionGate>,
      { session: null, api: { "GET /proxy/v1/session": { body: session } } },
    );
    expect(screen.getByRole("status")).toBeTruthy();
    expect(await screen.findByText("inside")).toBeTruthy();
  });

  it("TestGateRedirectsOnceOnUnauthenticated", async () => {
    const assign = vi.fn();
    renderPanel(
      <SessionGate>
        <p>inside</p>
      </SessionGate>,
      { session: null, route: "/p/web/vms", assign, api: { "GET /proxy/v1/session": { status: 401, body: { error: "unauthenticated" } } } },
    );
    await waitFor(() => expect(assign).toHaveBeenCalled());
    expect(assign).toHaveBeenCalledTimes(1);
    expect(assign).toHaveBeenCalledWith("/oauth/login?returnTo=%2Fp%2Fweb%2Fvms");
    expect(screen.queryByText("inside")).toBeNull();
  });

  it("TestRedirectLoopGuard", async () => {
    sessionStorage.setItem("bedrock.panel.signin", String(Date.now() - 5000));
    const assign = vi.fn();
    renderPanel(
      <SessionGate>
        <p>inside</p>
      </SessionGate>,
      { session: null, route: "/p/web/vms", assign, api: { "GET /proxy/v1/session": { status: 401, body: { error: "unauthenticated" } } } },
    );
    const retry = await screen.findByRole("link", { name: "Retry" });
    expect(retry.getAttribute("href")).toBe("/oauth/login?returnTo=%2Fp%2Fweb%2Fvms");
    expect(assign).not.toHaveBeenCalled();
  });

  it("TestSafeReturnTo", () => {
    expect(safeReturnTo("/p/web")).toBe("/p/web");
    expect(safeReturnTo("/a?b=1")).toBe("/a?b=1");
    for (const bad of ["//evil.test", "https://evil.test", "evil", "", "/\\evil"]) {
      expect(safeReturnTo(bad)).toBe("/");
    }
    expect(loginUrl("//evil.test")).toBe("/oauth/login?returnTo=%2F");
  });

  it("TestSessionErrorShowsRetry", async () => {
    const user = userEvent.setup();
    let calls = 0;
    renderPanel(
      <SessionGate>
        <p>inside</p>
      </SessionGate>,
      {
        session: null,
        api: {
          "GET /proxy/v1/session": () => {
            calls += 1;
            return calls === 1 ? { status: 500, body: { error: "upstream_unavailable" } } : { body: session };
          },
        },
      },
    );
    await user.click(await screen.findByRole("button", { name: "Retry" }));
    expect(await screen.findByText("inside")).toBeTruthy();
    expect(calls).toBe(2);
  });

  it("TestCsrfIsHeldInMemoryOnly", async () => {
    function Probe() {
      const query = useSession();
      return <p>{query.data?.user.name}</p>;
    }
    renderPanel(
      <SessionGate>
        <Probe />
      </SessionGate>,
      { session: null, api: { "GET /proxy/v1/session": { body: session } } },
    );
    await screen.findByText("admin");
    for (const store of [localStorage, sessionStorage]) {
      for (let index = 0; index < store.length; index += 1) {
        const key = store.key(index) ?? "";
        expect(store.getItem(key)).not.toContain(session.csrf);
        expect(key).not.toContain(session.csrf);
      }
    }
  });
});
