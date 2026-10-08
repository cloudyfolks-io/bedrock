import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { Check } from "../../api/types";
import { virtualMachines } from "../../test/fixtures";
import { renderPanel } from "../../test/renderPanel";
import VmsPage from "./VmsPage";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };

function render(allow: (check: Check) => boolean = () => true) {
  return renderPanel(
    <Routes>
      <Route path="/p/:project/vms" element={<VmsPage />} />
    </Routes>,
    {
      route: "/p/web/vms",
      allow,
      api: { "GET /proxy/v1/projects": { body: projects }, "GET /apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines": { body: virtualMachines() } },
    },
  );
}

describe("VmsPage", () => {
  it("shows the columns of a virtual machine", async () => {
    render();
    const row = (await screen.findByRole("link", { name: "web-1" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("Running")).toBeTruthy();
    expect(within(row).getByText("host-01")).toBeTruthy();
    expect(within(row).getByText("u1.medium")).toBeTruthy();
    expect(row.querySelector('bdi[dir="ltr"]')?.textContent).toContain("10.100.0.5");
  });

  it("filters by phase", async () => {
    const user = userEvent.setup();
    const { container } = render();
    await screen.findByRole("link", { name: "web-1" });
    await user.click(screen.getByRole("combobox", { name: /Phase/ }));
    await user.click(await screen.findByRole("option", { name: "Failed" }));
    await waitFor(() => expect(container.querySelectorAll("tbody tr")).toHaveLength(1));
    expect(screen.getByRole("link", { name: "old" })).toBeTruthy();
  });

  it("routes the create buttons when create is allowed", async () => {
    render();
    expect((await screen.findByRole("link", { name: "Create" })).getAttribute("href")).toBe("/p/web/vms/new");
    expect(screen.getByRole("link", { name: "Install from ISO" }).getAttribute("href")).toBe("/p/web/vms/new-iso");
  });

  it("hides the create buttons when create is not allowed", async () => {
    render((check) => check.verb !== "create");
    await screen.findByRole("link", { name: "web-1" });
    expect(screen.queryByRole("link", { name: "Create" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Install from ISO" })).toBeNull();
  });
});
