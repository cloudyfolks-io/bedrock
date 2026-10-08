import { screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Check } from "../api/types";
import { Can, checkKey } from "./authz";
import { renderPanel } from "../test/renderPanel";

const base = { group: "bedrock.cloudyfolks.io", namespace: "web" };
const create: Check = { ...base, verb: "create", resource: "virtualmachines" };
const del: Check = { ...base, verb: "delete", resource: "virtualmachines" };
const start: Check = { ...base, verb: "update", resource: "virtualmachines", subresource: "start" };

function authzCalls(calls: { path: string; body: unknown }[]) {
  return calls.filter((call) => call.path === "/proxy/v1/authz");
}

describe("authz", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("TestChecksOfOneRenderPassFormOneRequest", async () => {
    const { api } = renderPanel(
      <>
        {[create, create, create, del, del, start, start, start, create, del].map((check, index) => (
          <Can key={index} check={check}>
            <span>{`ok-${index}`}</span>
          </Can>
        ))}
      </>,
      { allow: () => true },
    );
    await screen.findByText("ok-0");
    const calls = authzCalls(api.calls);
    expect(calls).toHaveLength(1);
    expect((calls[0]?.body as { checks: Check[] }).checks).toHaveLength(3);
  });

  it("TestHiddenWhilePendingAndOnDenied", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    renderPanel(
      <>
        <Can check={create}>
          <span>can-create</span>
        </Can>
        <Can check={del}>
          <span>can-delete</span>
        </Can>
      </>,
      {
        api: {
          "POST /proxy/v1/authz": async () => {
            await gate;
            return { body: { results: [{ allowed: true }, { allowed: false, reason: "no" }] } };
          },
        },
      },
    );
    expect(screen.queryByText("can-create")).toBeNull();
    release();
    expect(await screen.findByText("can-create")).toBeTruthy();
    expect(screen.queryByText("can-delete")).toBeNull();
  });

  it("TestHiddenOnRequestError", async () => {
    const { api } = renderPanel(
      <Can check={create}>
        <span>can-create</span>
      </Can>,
      { api: { "POST /proxy/v1/authz": { status: 502, body: { error: "upstream_unavailable" } } } },
    );
    await waitFor(() => expect(authzCalls(api.calls)).toHaveLength(1));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.queryByText("can-create")).toBeNull();
  });

  it("TestResultsAreCached", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    const view = renderPanel(
      <Can check={create}>
        <span>can-create</span>
      </Can>,
      { allow: () => true },
    );
    await screen.findByText("can-create");
    view.rerender(<span>none</span>);
    view.rerender(
      <Can check={create}>
        <span>can-create</span>
      </Can>,
    );
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(authzCalls(view.api.calls)).toHaveLength(1);
    vi.setSystemTime(Date.now() + 31_000);
    view.rerender(<span>none</span>);
    view.rerender(
      <Can check={create}>
        <span>can-create</span>
      </Can>,
    );
    await waitFor(() => expect(authzCalls(view.api.calls)).toHaveLength(2));
  });

  it("TestChunksOfOneHundred", async () => {
    const checks: Check[] = Array.from({ length: 250 }, (_, index) => ({ ...base, verb: "get", resource: "virtualmachines", name: `vm-${index}` }));
    const { api } = renderPanel(
      <>
        {checks.map((check) => (
          <Can key={check.name} check={check}>
            <span>{check.name}</span>
          </Can>
        ))}
      </>,
      { allow: () => true },
    );
    await screen.findByText("vm-249");
    expect(authzCalls(api.calls).map((call) => (call.body as { checks: Check[] }).checks.length).sort((a, b) => b - a)).toEqual([100, 100, 50]);
  });

  it("TestNamespaceIsPartOfTheKey", () => {
    expect(checkKey({ ...create, namespace: "a" })).not.toBe(checkKey({ ...create, namespace: "b" }));
    expect(checkKey(create)).toBe(checkKey({ ...create }));
  });

  it("TestSessionChangeDropsTheCache", async () => {
    const view = renderPanel(
      <Can check={create}>
        <span>can-create</span>
      </Can>,
      { allow: () => true },
    );
    await screen.findByText("can-create");
    view.queryClient.setQueryData(["session"], { ...view.queryClient.getQueryData<object>(["session"]), user: { name: "other", displayName: "O", email: "o@x", groups: [] } });
    await waitFor(() => expect(authzCalls(view.api.calls)).toHaveLength(2));
  });

  it("TestFailedAuthzIsNotCachedAsDenied fails closed and retries", async () => {
    let calls = 0;
    renderPanel(
      <Can check={create}>
        <span>can-create</span>
      </Can>,
      {
        api: {
          "POST /proxy/v1/authz": () => {
            calls += 1;
            return calls === 1 ? { status: 502, body: { error: "upstream_unavailable" } } : { body: { results: [{ allowed: true }] } };
          },
        },
      },
    );
    await waitFor(() => expect(calls).toBe(1));
    expect(screen.queryByText("can-create")).toBeNull();
    expect(await screen.findByText("can-create", undefined, { timeout: 4000 })).toBeTruthy();
    expect(calls).toBeGreaterThanOrEqual(2);
  });

  it("TestNoRetryTimerSurvivesUnmount", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "queueMicrotask"] });
    const view = renderPanel(
      <Can check={create}>
        <span>can-create</span>
      </Can>,
      { api: { "POST /proxy/v1/authz": { status: 502, body: { error: "upstream_unavailable" } } } },
    );
    const started: { id: unknown; delay: unknown }[] = [];
    const realSet = globalThis.setTimeout;
    const setSpy = vi.spyOn(globalThis, "setTimeout").mockImplementation(((handler: () => void, delay?: number) => {
      const id = realSet(handler, delay);
      started.push({ id, delay });
      return id;
    }) as unknown as typeof setTimeout);
    const clearSpy = vi.spyOn(globalThis, "clearTimeout");
    for (let tick = 0; tick < 3; tick += 1) {
      await vi.advanceTimersByTimeAsync(100);
    }
    expect(authzCalls(view.api.calls)).toHaveLength(1);
    const retry = started.find((entry) => entry.delay === 1000);
    expect(retry).toBeTruthy();
    view.unmount();
    expect(clearSpy).toHaveBeenCalledWith(retry?.id);
    setSpy.mockRestore();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(authzCalls(view.api.calls)).toHaveLength(1);
  });
});
