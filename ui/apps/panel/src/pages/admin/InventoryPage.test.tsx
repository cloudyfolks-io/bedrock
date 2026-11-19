import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { AppRoutes } from "../../shell/routes";
import { renderPanel } from "../../test/renderPanel";
import type { FakeRoute } from "../../test/fakeApi";

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

  it("uses continuation tokens and recovers when a list expires", async () => {
    const user = userEvent.setup();
    const token = "opaque/?=&";
    const { api } = show("/admin/inventory", {
      [`GET ${allVMs}`]: ({ query }) => query.has("continue")
        ? { status: 410, body: { kind: "Status", reason: "Expired" } }
        : { body: { metadata: { continue: token }, items: [machine("worker", "platform-staging")] } },
    });
    await user.click(await screen.findByRole("button", { name: "Next" }, inventoryWait));
    expect(await screen.findByRole("heading", { name: "This list expired" }, inventoryWait)).toBeTruthy();
    expect(api.calls.find((call) => call.path === allVMs && call.query.has("continue"))?.query.get("continue")).toBe(token);
    await user.click(screen.getByRole("button", { name: "Go to first page" }));
    expect(await screen.findByRole("link", { name: "worker" }, inventoryWait)).toBeTruthy();
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
