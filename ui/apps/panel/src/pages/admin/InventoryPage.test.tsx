import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { AppRoutes } from "../../shell/routes";
import { renderPanel } from "../../test/renderPanel";
import type { FakeReply, FakeRoute } from "../../test/fakeApi";

const apiRoot = "/apis/bedrock.cloudyfolks.io/v1alpha1";
const allVMs = `${apiRoot}/virtualmachines`;
const inventoryWait = { timeout: 5000 };
const projects = { projects: [
  { name: "staging", displayName: "Staging", tenant: "platform", namespace: "platform-staging" },
  { name: "tools", displayName: "Tools", tenant: "platform", namespace: "platform-tools" },
  { name: "pending", displayName: "Pending", tenant: "platform", namespace: "" },
] };

function machine(name: string, namespace: string) {
  return { metadata: { name, namespace }, status: { phase: "Running" } };
}

function show(route: string, routes: Record<string, FakeRoute> = {}) {
  return renderPanel(<AppRoutes />, { route, api: { "GET /proxy/v1/projects": { body: projects }, ...routes } });
}

describe("admin inventory", () => {
  it("shows matching names in different projects and resources outside projects", async () => {
    const first = machine("worker", "platform-staging");
    const second = machine("worker", "platform-tools");
    const platform = machine("operator-node", "bedrock-system");
    const { api } = show("/admin", { [`GET ${allVMs}`]: { body: { metadata: {}, items: [first, second, platform] } } });
    const table = await screen.findByRole("table", { name: "Virtual machines" }, inventoryWait);
    expect(screen.getByRole("heading", { name: "Resource inventory", level: 1 })).toBeTruthy();
    expect(within(table).getAllByRole("link", { name: "worker" }).map((link) => link.getAttribute("href"))).toEqual(["/p/staging/vms/worker", "/p/tools/vms/worker"]);
    const unmatched = within(table).getByText("operator-node").closest("tr") as HTMLElement;
    expect(within(unmatched).getByText("No linked project")).toBeTruthy();
    expect(within(unmatched).getByText("bedrock-system")).toBeTruthy();
    expect(api.calls.find((call) => call.path === allVMs)?.query.get("limit")).toBe("50");
  });

  it("applies the project and exact name to the API request", async () => {
    const user = userEvent.setup();
    const namespaceList = `${apiRoot}/namespaces/platform-tools/virtualmachines`;
    const { api } = show("/admin/inventory?project=tools", { [`GET ${namespaceList}`]: { body: { metadata: {}, items: [machine("worker", "platform-tools")] } } });
    await screen.findByRole("link", { name: "worker" }, inventoryWait);
    await user.type(screen.getByRole("textbox", { name: "Exact resource name" }), "worker");
    await user.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(api.calls.find((call) => call.path === namespaceList && call.query.has("fieldSelector"))?.query.get("fieldSelector")).toBe("metadata.name=worker"), inventoryWait);
    expect(api.calls.some((call) => call.path === allVMs)).toBe(false);
  });

  it("does not broaden a request for an unavailable or unready project", async () => {
    const pending = show("/admin/inventory?project=pending");
    expect(await screen.findByRole("heading", { name: "The project namespace is not ready" }, inventoryWait)).toBeTruthy();
    expect(pending.api.calls.some((call) => call.path.startsWith(apiRoot))).toBe(false);
    pending.unmount();
    const missing = show("/admin/inventory?project=missing");
    expect(await screen.findByRole("heading", { name: "This project is unavailable" }, inventoryWait)).toBeTruthy();
    expect(missing.api.calls.some((call) => call.path.startsWith(apiRoot))).toBe(false);
  });

  it("blocks a project filter for Networks until the user clears it", async () => {
    const user = userEvent.setup();
    const networks = `${apiRoot}/networks`;
    const selected = show("/admin/inventory?kind=networks&project=tools", {
      [`GET ${networks}`]: { body: { metadata: {}, items: [{ metadata: { name: "tenant-network" }, spec: { tenantRef: "platform" } }] } },
    });
    expect(await screen.findByRole("heading", { name: "Networks use tenant scope" }, inventoryWait)).toBeTruthy();
    await waitFor(() => expect(selected.api.calls.some((call) => call.path === "/proxy/v1/projects")).toBe(true), inventoryWait);
    expect(selected.api.calls.some((call) => call.path.startsWith(apiRoot))).toBe(false);
    await user.click(screen.getByRole("button", { name: "Clear project filter" }));
    expect(await screen.findByText("tenant-network", {}, inventoryWait)).toBeTruthy();
    expect(selected.api.calls.filter((call) => call.path === networks)).toHaveLength(1);
    selected.unmount();

    const missing = show("/admin/inventory?kind=networks&project=missing");
    expect(await screen.findByRole("button", { name: "Clear project filter" }, inventoryWait)).toBeTruthy();
    await waitFor(() => expect(missing.api.calls.some((call) => call.path === "/proxy/v1/projects")).toBe(true), inventoryWait);
    expect(missing.api.calls.some((call) => call.path.startsWith(apiRoot))).toBe(false);
  });

  it("lists restore records as a separate resource type within a project", async () => {
    const restores = `${apiRoot}/namespaces/platform-tools/virtualmachinerestores`;
    const { api } = show("/admin/inventory?kind=restores&project=tools", {
      [`GET ${restores}`]: { body: { metadata: {}, items: [machine("restore-worker", "platform-tools")] } },
    });
    const table = await screen.findByRole("table", { name: "Virtual machine restores" }, inventoryWait);
    expect(within(table).getByRole("link", { name: "restore-worker" }).getAttribute("href")).toBe("/p/tools/backups");
    expect(api.calls.filter((call) => call.path.startsWith(apiRoot)).map((call) => call.path)).toEqual([restores]);
  });

  it("shows an empty page without claiming that the full list is empty", async () => {
    const user = userEvent.setup();
    const { api } = show("/admin/inventory?name=worker", {
      [`GET ${allVMs}`]: ({ query }) => query.has("continue")
        ? { body: { metadata: {}, items: [machine("worker", "platform-staging")] } }
        : { body: { metadata: { continue: "empty-page-token" }, items: [] } },
    });
    expect(await screen.findByRole("heading", { name: "No resources match on this page" }, inventoryWait)).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "No resources match" })).toBeNull();
    const pagesNav = screen.getByRole("navigation", { name: "Inventory pages" });
    expect(within(pagesNav).getByRole("status").textContent).toBe("Page 1");
    await user.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByRole("link", { name: "worker" }, inventoryWait)).toBeTruthy();
    expect(within(pagesNav).getByRole("status").textContent).toBe("Page 2");
    await user.click(screen.getByRole("button", { name: "Previous" }));
    expect(screen.getByRole("heading", { name: "No resources match on this page" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByRole("link", { name: "worker" }, inventoryWait)).toBeTruthy();
    const pages = api.calls.filter((call) => call.path === allVMs);
    expect(pages).toHaveLength(2);
    expect(pages.every((call) => call.query.get("fieldSelector") === "metadata.name=worker")).toBe(true);
  });

  it("uses a fresh first page and token after a list expires", async () => {
    const user = userEvent.setup();
    const oldToken = "opaque/?=&";
    const newToken = "new/?=&";
    let roots = 0;
    let finishRefresh = () => {};
    const freshHead = new Promise<FakeReply>((resolve) => {
      finishRefresh = () => resolve({ body: { metadata: { continue: newToken }, items: [machine("fresh-worker", "platform-tools")] } });
    });
    const { api } = show("/admin/inventory", {
      [`GET ${allVMs}`]: ({ query }) => {
        const cursor = query.get("continue");
        if (cursor === oldToken) {
          return { status: 410, body: { kind: "Status", reason: "Expired" } };
        }
        if (cursor === newToken) {
          return { body: { metadata: {}, items: [machine("next-worker", "platform-tools")] } };
        }
        return ++roots === 1 ? { body: { metadata: { continue: oldToken }, items: [machine("old-worker", "platform-staging")] } } : freshHead;
      },
    });
    await user.click(await screen.findByRole("button", { name: "Next" }, inventoryWait));
    expect(await screen.findByRole("heading", { name: "This list expired" }, inventoryWait)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Go to first page" }));
    await waitFor(() => expect(roots).toBe(2), inventoryWait);
    expect(screen.queryByRole("button", { name: "Next" })).toBeNull();
    expect(screen.queryByRole("link", { name: "old-worker" })).toBeNull();
    finishRefresh();
    expect(await screen.findByRole("link", { name: "fresh-worker" }, inventoryWait)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByRole("link", { name: "next-worker" }, inventoryWait)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Refresh list" }));
    expect(await screen.findByRole("link", { name: "fresh-worker" }, inventoryWait)).toBeTruthy();
    expect(roots).toBe(3);
    expect(api.calls.filter((call) => call.path === allVMs && call.query.has("continue")).map((call) => call.query.get("continue"))).toEqual([oldToken, newToken]);
  });

  it("shows a retry for a failed list and a distinct permission error", async () => {
    const user = userEvent.setup();
    let status = 503;
    show("/admin/inventory", { [`GET ${allVMs}`]: () => ({ status, body: { error: "failure" } }) });
    expect(await screen.findByRole("alert", {}, inventoryWait)).toBeTruthy();
    expect(screen.getByText("The panel could not load this data.")).toBeTruthy();
    status = 403;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("You cannot view this", {}, inventoryWait)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  });
});
