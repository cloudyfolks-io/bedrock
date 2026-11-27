import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { KubeList, KubeObject } from "../../api/types";
import hosts from "../../fixtures/hosts.json";
import { openApiRoutes } from "../../test/openapi";
import { renderPanel } from "../../test/renderPanel";
import type { Check } from "../../api/types";
import HostDetailPage from "./HostDetailPage";

const items = (hosts as unknown as KubeList).items;

function host(name: string): KubeObject {
  const found = items.find((item) => item.metadata.name === name);
  if (!found) {
    throw new Error("fixture");
  }
  return found;
}

function render(object: KubeObject, allow: (check: Check) => boolean = () => true, route = `/admin/hosts/${object.metadata.name}`) {
  return renderPanel(
    <Routes>
      <Route path="/admin/hosts/:name" element={<HostDetailPage />} />
    </Routes>,
    {
      route,
      allow,
      api: {
        ...openApiRoutes(),
        [`GET /apis/bedrock.cloudyfolks.io/v1alpha1/hosts/${object.metadata.name}`]: { body: object },
        [`PATCH /apis/bedrock.cloudyfolks.io/v1alpha1/hosts/${object.metadata.name}`]: { body: object },
      },
    },
  );
}

describe("HostDetailPage", () => {
  it("shows the overview with condition text", async () => {
    render(host("host-01"));
    expect(await screen.findByRole("heading", { level: 1, name: "host-01" })).toBeTruthy();
    expect(screen.getByText("control-plane")).toBeTruthy();
    expect(screen.getByText("Sun 02:00-04:00")).toBeTruthy();
    expect(screen.getByText("v1.36.0")).toBeTruthy();
    for (const type of ["Ready", "ManagementApplied", "MaintenanceMode", "RebootPending"]) {
      expect(screen.getAllByText(type).length).toBeGreaterThan(0);
    }
    expect(screen.getByText("bond0 up")).toBeTruthy();
  });

  it("shows labeled inventory fields and keeps identifiers left to right", async () => {
    const user = userEvent.setup();
    render(host("host-01"), () => true, "/admin/hosts/host-01?tab=unknown");
    const overview = await screen.findByRole("tab", { name: "Overview", selected: true });
    overview.focus();
    await user.keyboard("{ArrowRight}");
    expect(document.activeElement).toBe(screen.getByRole("tab", { name: "Inventory" }));
    await user.keyboard("{Enter}");
    const disks = within(await screen.findByRole("region", { name: "Disks" }));
    expect(disks.getByText("/dev/nvme0n1").getAttribute("dir")).toBe("ltr");
    expect(disks.getByText("Path")).toBeTruthy();
    expect(screen.getByText("894.3 GiB")).toBeTruthy();
    expect(screen.getByText("3c:ec:ef:00:00:01")).toBeTruthy();
    expect(screen.getAllByText("0000:41:00.0").length).toBeGreaterThan(0);
    expect(screen.getByText("256 GiB")).toBeTruthy();
    expect(screen.getByText("AMD EPYC 7543")).toBeTruthy();
  });

  it("keeps readiness and controller details above all tabs", async () => {
    const user = userEvent.setup();
    render(host("host-03"), () => false, "/admin/hosts/host-03?tab=inventory");
    const status = (await screen.findByText("AgentNotReporting")).closest<HTMLElement>('[role="status"]')!;
    expect(status.closest("header")).toBe(screen.getByRole("heading", { level: 1 }).closest("header"));
    expect(within(status).getByText("Not ready")).toBeTruthy();
    expect(within(status).getByText("no heartbeat for 5m")).toBeTruthy();
    expect(status.compareDocumentPosition(screen.getByRole("tablist")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    for (const tab of ["Overview", "Metrics", "Inventory"]) {
      await user.click(screen.getByRole("tab", { name: tab }));
      expect(screen.getByRole("tab", { name: tab, selected: true })).toBeTruthy();
      expect(within(status).getByText("AgentNotReporting")).toBeTruthy();
    }
  });

  it("names unreported inventory and configuration instead of showing empty tables", async () => {
    const user = userEvent.setup();
    render({ ...host("host-03"), status: {} });
    expect(await screen.findByText("No configuration steps reported.")).toBeTruthy();
    await user.click(screen.getByRole("tab", { name: "Inventory" }));
    expect(screen.getByText("No CPU or memory data reported.")).toBeTruthy();
    expect(screen.getByText("No disks reported.")).toBeTruthy();
    expect(screen.getByText("No network interfaces reported.")).toBeTruthy();
    expect(screen.getByText("No PCI devices reported.")).toBeTruthy();
    expect(screen.getByText("No firmware or system data reported.")).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("TestMaintenanceToggleUsesMergePatch", async () => {
    const user = userEvent.setup();
    const { api } = render(host("host-01"));
    await user.click(await screen.findByRole("switch", { name: "Maintenance mode" }));
    expect(await screen.findByText(/noout/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Enter maintenance" }));
    await waitFor(() => expect(api.calls.some((call) => call.method === "PATCH")).toBe(true));
    const patch = api.calls.find((call) => call.method === "PATCH");
    expect(patch?.body).toEqual({ spec: { maintenance: true } });
    expect(patch?.headers.get("Content-Type")).toBe("application/merge-patch+json");
  });

  it("hides the switch without update permission and disables it while pending", async () => {
    const denied = render(host("host-01"), () => false);
    await screen.findByRole("heading", { level: 1, name: "host-01" });
    await waitFor(() => expect(denied.api.calls.some((call) => call.path === "/proxy/v1/authz")).toBe(true));
    expect(screen.queryByRole("switch", { name: "Maintenance mode" })).toBeNull();
    denied.unmount();
    const pending = { ...host("host-01"), spec: { ...host("host-01").spec, maintenance: true } };
    render(pending);
    const toggle = await screen.findByRole("switch", { name: "Maintenance mode" });
    expect(toggle.getAttribute("data-disabled")).not.toBeNull();
  });

  it("TestRolesEditUsesTheForm", async () => {
    const user = userEvent.setup();
    render(host("host-01"));
    await user.click(await screen.findByRole("button", { name: "Edit roles" }));
    expect(await screen.findByRole("dialog", { name: "Edit host-01" }, { timeout: 15000 })).toBeTruthy();
  });

  it("has a lazy metrics tab", async () => {
    const user = userEvent.setup();
    const { api } = render(host("host-01"));
    await user.click(await screen.findByRole("tab", { name: "Metrics" }));
    expect(await screen.findByRole("heading", { level: 3, name: "CPU use" })).toBeTruthy();
    await waitFor(() => expect(api.calls.some((call) => call.path === "/proxy/v1/metrics/range" && call.query.get("query") === "host.cpu" && call.query.get("name") === "host-01")).toBe(true));
  });
});
