import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { Check } from "../api/types";
import { createFakeSockets } from "../test/fakeSocket";
import { virtualMachines } from "../test/fixtures";
import { renderPanel, type RenderOptions } from "../test/renderPanel";
import { definitionFor } from "./registry";
import { ResourceListPage } from "./ResourceListPage";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const vmList = "GET /apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines";
const hello = { type: "hello", expiresAt: "2026-10-09T00:00:00Z", limits: { maxSubscriptions: 64 } };

function renderVms(options: RenderOptions = {}) {
  const def = definitionFor("virtualmachines");
  if (!def) {
    throw new Error("missing definition");
  }
  return renderPanel(
    <Routes>
      <Route path="/p/:project/vms" element={<ResourceListPage def={def} />} />
    </Routes>,
    {
      route: "/p/web/vms",
      ...options,
      api: { "GET /proxy/v1/projects": { body: projects }, [vmList]: { body: virtualMachines() }, ...options.api },
    },
  );
}

function authzCalls(calls: { path: string }[]) {
  return calls.filter((call) => call.path === "/proxy/v1/authz");
}

describe("ResourceListPage", () => {
  it("TestListRendersRows", async () => {
    const { container } = renderVms();
    expect(await screen.findByRole("link", { name: "web-1" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "db-1" })).toBeTruthy();
    const row = screen.getByRole("link", { name: "web-1" }).closest("tr") as HTMLElement;
    expect(within(row).getByText("Running")).toBeTruthy();
    expect(within(row).getByText("host-01")).toBeTruthy();
    expect(row.querySelector('bdi[dir="ltr"]')?.textContent).toContain("10.100.0.5");
    expect(container.querySelectorAll("tbody tr")).toHaveLength(3);
    expect(screen.getByRole("link", { name: "web-1" }).getAttribute("href")).toBe("/p/web/vms/web-1");
  });

  it("TestLiveEventsUpdateRows", async () => {
    const sockets = createFakeSockets();
    const { api } = renderVms({ socket: sockets });
    await screen.findByRole("link", { name: "web-1" });
    await waitFor(() => expect(sockets.sockets).toHaveLength(1));
    const socket = sockets.latest();
    act(() => {
      socket.open();
      socket.receive(hello);
    });
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(1));
    const id = socket.framesOf("subscribe")[0]?.id;
    const web1 = virtualMachines().items.find((item) => item.metadata.name === "web-1");
    if (!web1) {
      throw new Error("fixture");
    }
    act(() => socket.receive({ type: "event", id, event: "MODIFIED", resourceVersion: "101", object: { ...web1, status: { ...web1.status, phase: "Stopping" } } }));
    expect(await screen.findByText("Stopping")).toBeTruthy();
    act(() => socket.receive({ type: "event", id, event: "ADDED", resourceVersion: "102", object: { ...web1, metadata: { ...web1.metadata, name: "new-1", uid: "uid-new" } } }));
    expect(await screen.findByRole("link", { name: "new-1" })).toBeTruthy();
    act(() => socket.receive({ type: "event", id, event: "DELETED", resourceVersion: "103", object: web1 }));
    await waitFor(() => expect(screen.queryByRole("link", { name: "web-1" })).toBeNull());
    expect(api.calls.filter((call) => call.path.endsWith("/virtualmachines"))).toHaveLength(1);
  });

  it("TestSortingAndFilter", async () => {
    const user = userEvent.setup();
    const { container } = renderVms();
    await screen.findByRole("link", { name: "web-1" });
    const names = () => [...container.querySelectorAll("tbody tr td:first-child")].map((cell) => cell.textContent);
    expect(names()).toEqual(["db-1", "old", "web-1"]);
    await user.click(screen.getByRole("button", { name: "Name" }));
    expect(screen.getByRole("columnheader", { name: "Name" }).getAttribute("aria-sort")).toBe("descending");
    expect(names()).toEqual(["web-1", "old", "db-1"]);
    await user.type(screen.getByLabelText("Filter by name"), "web");
    expect(names()).toEqual(["web-1"]);
    await user.clear(screen.getByLabelText("Filter by name"));
    await user.type(screen.getByLabelText("Filter by name"), "zzz");
    expect(await screen.findByText("No match")).toBeTruthy();
  });

  it("TestForbiddenListShowsNoAccess", async () => {
    renderVms({ api: { [vmList]: { status: 403, body: { error: "forbidden" } } } });
    expect(await screen.findByText("You cannot view this")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("TestEmptyState", async () => {
    const empty = { items: [], metadata: { resourceVersion: "1" } };
    const denied = renderVms({ allow: () => false, api: { [vmList]: { body: empty } } });
    expect(await screen.findByText("Nothing here yet")).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Create" })).toBeNull();
    denied.unmount();
    renderVms({ allow: (check: Check) => check.verb === "create", api: { [vmList]: { body: empty } } });
    expect((await screen.findAllByRole("link", { name: "Create" })).length).toBeGreaterThan(0);
  });

  it("TestCreateAndRowActionsAreHiddenUntilAllowed", async () => {
    const user = userEvent.setup();
    const none = renderVms({ allow: () => false });
    await screen.findByRole("link", { name: "web-1" });
    await waitFor(() => expect(authzCalls(none.api.calls)).toHaveLength(1));
    expect(screen.queryByRole("link", { name: "Create" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Actions for/ })).toBeNull();
    none.unmount();

    const some = renderVms({ allow: (check: Check) => check.verb === "create" || check.verb === "delete" });
    await screen.findByRole("link", { name: "web-1" });
    expect(await screen.findByRole("link", { name: "Create" })).toBeTruthy();
    await user.click(await screen.findByRole("button", { name: "Actions for web-1" }));
    expect(await screen.findByRole("menuitem", { name: "Delete" })).toBeTruthy();
    expect(screen.queryByRole("menuitem", { name: "Start" })).toBeNull();
    expect(authzCalls(some.api.calls)).toHaveLength(1);
  });

  it("TestRtlListKeepsTechnicalTextLtr", async () => {
    const { container } = renderVms({ locale: "fa" });
    await screen.findByRole("link", { name: "web-1" });
    expect(container.querySelector('bdi[dir="ltr"]')?.textContent).toContain("10.100.0.5");
    expect(container.querySelector("th.br-th")).toBeTruthy();
    expect(document.documentElement.dir).toBe("rtl");
  });

  it("TestSnapshotHasNoSecretColumns", () => {
    const def = definitionFor("volumesnapshots");
    expect(def?.columns.map((column) => column.id).join(",")).not.toMatch(/secret|token|key|password/i);
  });

  it("TestLiveIndicatorShowsPausedOnStreamError", async () => {
    const sockets = createFakeSockets();
    renderVms({ socket: sockets });
    await screen.findByRole("link", { name: "web-1" });
    await waitFor(() => expect(sockets.sockets).toHaveLength(1));
    const socket = sockets.latest();
    act(() => {
      socket.open();
      socket.receive(hello);
    });
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(1));
    act(() => socket.receive({ type: "error", id: socket.framesOf("subscribe")[0]?.id, code: "forbidden", message: "no" }));
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe("Paused"));
  });
});
