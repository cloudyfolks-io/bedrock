import { screen, waitFor } from "@testing-library/react";
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

  it("shows the inventory tables", async () => {
    const user = userEvent.setup();
    render(host("host-01"));
    await user.click(await screen.findByRole("tab", { name: "Inventory" }));
    expect(await screen.findByText("/dev/nvme0n1")).toBeTruthy();
    expect(screen.getByText("894.3 GiB")).toBeTruthy();
    expect(screen.getByText("3c:ec:ef:00:00:01")).toBeTruthy();
    expect(screen.getAllByText("0000:41:00.0").length).toBeGreaterThan(0);
    expect(screen.getByText("256 GiB")).toBeTruthy();
    expect(screen.getByText("AMD EPYC 7543")).toBeTruthy();
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
    render(host("host-01"));
    await user.click(await screen.findByRole("tab", { name: "Metrics" }));
    expect(await screen.findByText("Metrics", { selector: "p" })).toBeTruthy();
  });
});
