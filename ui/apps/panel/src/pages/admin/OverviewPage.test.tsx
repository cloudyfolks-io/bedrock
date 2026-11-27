import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import hosts from "../../fixtures/hosts.json";
import tenants from "../../fixtures/tenants.json";
import type { FakeRoute } from "../../test/fakeApi";
import { renderPanel } from "../../test/renderPanel";
import OverviewPage from "./OverviewPage";

const base = "GET /apis/bedrock.cloudyfolks.io/v1alpha1";
const empty = { metadata: {}, items: [] };

function show(hostsReply: FakeRoute = { body: hosts }) {
  return renderPanel(<OverviewPage />, {
    route: "/admin/overview",
    api: {
      [`${base}/hosts`]: hostsReply,
      [`${base}/tenants`]: { body: tenants },
      [`${base}/clusterimages`]: { body: empty },
      [`${base}/addons`]: { body: empty },
    },
  });
}

function hostPanel() {
  return within(screen.getByRole("heading", { name: "Hosts at a glance" }).closest("section")!);
}

describe("admin overview", () => {
  it("groups the four live inventory counts and links to existing drilldowns", async () => {
    show();
    const summary = within(screen.getByRole("region", { name: "Resource summary" }));
    expect(summary.getAllByRole("listitem")).toHaveLength(4);
    expect((await summary.findByRole("link", { name: "3 Hosts" })).getAttribute("href")).toBe("/admin/hosts");
    expect((await summary.findByRole("link", { name: "2 Tenants" })).getAttribute("href")).toBe("/admin/tenants");
    expect((await summary.findByRole("link", { name: "0 Catalog images" })).getAttribute("href")).toBe("/admin/cluster-images");
    expect((await summary.findByRole("link", { name: "0 Addons" })).getAttribute("href")).toBe("/admin/addons");
    const inventory = screen.getByRole("link", { name: "Resource inventory" });
    expect(inventory.getAttribute("href")).toBe("/admin/inventory");
    expect(inventory.classList.contains("br-button--primary")).toBe(true);
  });

  it("shows a host table with roles and status alongside exact readiness", async () => {
    show();
    const table = within(await hostPanel().findByRole("table", { name: "Hosts at a glance" }));
    expect(hostPanel().getByRole("region", { name: "Hosts at a glance" }).tabIndex).toBe(0);
    expect(table.getAllByRole("columnheader").map((node) => node.textContent)).toEqual(["Name", "Roles", "Status"]);
    expect(table.getAllByRole("rowheader").map((node) => node.textContent)).toEqual(["host-01", "host-02", "host-03"]);
    const host = within(table.getByRole("row", { name: /host-03/ }));
    expect(host.getByRole("link", { name: "host-03" }).getAttribute("href")).toBe("/admin/hosts/host-03");
    expect(host.getByRole("cell", { name: "workload · fabric-gateway" })).toBeTruthy();
    expect(host.getByRole("cell", { name: "Not ready" })).toBeTruthy();
    expect(hostPanel().getByRole("link", { name: "View all hosts" }).getAttribute("href")).toBe("/admin/hosts");
    const readiness = screen.getByRole("heading", { name: "Host readiness" }).closest("section")!;
    expect(within(readiness).getByText("2 / 3")).toBeTruthy();
    expect(within(readiness).getAllByRole("term").map((node) => node.textContent)).toEqual(["Ready", "Not ready", "Unknown"]);
    expect(within(readiness).getAllByRole("definition").map((node) => node.textContent)).toEqual(["2", "1", "0"]);
    expect(Array.from(readiness.querySelectorAll<HTMLElement>('[aria-hidden="true"] > [data-tone]'), (segment) => segment.style.flexGrow)).toEqual(["2", "1"]);
  });

  it("keeps status counts without claiming readiness for an empty host inventory", async () => {
    show({ body: empty });
    expect(await screen.findByRole("link", { name: "0 Hosts" })).toBeTruthy();
    expect(hostPanel().getByText("No hosts registered.")).toBeTruthy();
    expect(hostPanel().queryByRole("table")).toBeNull();
    const readiness = (await screen.findByRole("heading", { name: "Host readiness" })).closest("section")!;
    expect(within(readiness).getByText("No hosts registered.")).toBeTruthy();
    expect(within(readiness).queryByText("0 / 0")).toBeNull();
    expect(within(readiness).getAllByRole("definition").map((node) => node.textContent)).toEqual(["0", "0", "0"]);
  });

  it("reports loading in the host panel without showing host data", async () => {
    show(() => new Promise(() => {}));
    expect(await screen.findByRole("link", { name: "2 Tenants" })).toBeTruthy();
    expect(hostPanel().getByRole("status").textContent).toBe("Loading");
    expect(hostPanel().queryByRole("table")).toBeNull();
    expect(screen.queryByRole("heading", { name: "Host readiness" })).toBeNull();
  });

  it("does not show a zero count or host links when access is restricted", async () => {
    show({ status: 403, body: { kind: "Status", reason: "Forbidden" } });
    expect(await hostPanel().findByText("Access restricted")).toBeTruthy();
    expect(screen.getAllByText("Access restricted")).toHaveLength(1);
    expect(hostPanel().queryByRole("button", { name: "Retry" })).toBeNull();
    expect(hostPanel().queryByRole("table")).toBeNull();
    expect(screen.queryByRole("link", { name: /hosts/i })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Host readiness" })).toBeNull();
    expect(await screen.findByRole("link", { name: "2 Tenants" })).toBeTruthy();
  });

  it("reports unavailable hosts once and retries from the host panel", async () => {
    let requests = 0;
    show(() => ++requests === 1 ? { status: 503, body: { kind: "Status", reason: "ServiceUnavailable" } } : { body: hosts });
    expect((await hostPanel().findByText("Data unavailable")).getAttribute("role")).toBe("status");
    expect(screen.getAllByText("Data unavailable")).toHaveLength(1);
    expect(hostPanel().queryByRole("table")).toBeNull();
    expect(screen.queryByRole("heading", { name: "Host readiness" })).toBeNull();
    await userEvent.setup().click(hostPanel().getByRole("button", { name: "Retry" }));
    expect(await hostPanel().findByRole("table")).toBeTruthy();
    expect(await screen.findByRole("link", { name: "3 Hosts" })).toBeTruthy();
  });
});
