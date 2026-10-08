import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { KubeList } from "../../api/types";
import projects from "../../fixtures/projects.json";
import tenants from "../../fixtures/tenants.json";
import { openApiRoutes } from "../../test/openapi";
import { renderPanel } from "../../test/renderPanel";
import TenantDetailPage from "./TenantDetailPage";

const base = "/apis/bedrock.cloudyfolks.io/v1alpha1";
const acme = (tenants as unknown as KubeList).items[0];

function render() {
  return renderPanel(
    <Routes>
      <Route path="/admin/tenants/:name" element={<TenantDetailPage />} />
    </Routes>,
    {
      route: "/admin/tenants/acme",
      api: {
        ...openApiRoutes(),
        [`GET ${base}/tenants/acme`]: { body: acme },
        [`GET ${base}/projects`]: { body: projects as unknown as KubeList },
        [`GET ${base}/projects/web`]: { body: (projects as unknown as KubeList).items[1] },
        [`DELETE ${base}/projects/web`]: { body: {} },
      },
    },
  );
}

describe("TenantDetailPage", () => {
  it("shows the tenant, quota use and its projects", async () => {
    render();
    expect(await screen.findByRole("heading", { level: 1, name: "Acme Corp" })).toBeTruthy();
    expect(screen.getByText("10.100.0.0/16")).toBeTruthy();
    const bars = screen.getAllByRole("progressbar");
    expect(bars.length).toBeGreaterThan(0);
    expect(screen.getByRole("progressbar", { name: /cpu: 12 \/ 64/ }).getAttribute("aria-valuenow")).toBe("12");
    const table = screen.getByRole("table", { name: "Projects" });
    expect(within(table).getByText("web")).toBeTruthy();
    expect(within(table).getByText("acme-db")).toBeTruthy();
  });

  it("TestProjectMembersEditor opens the edit form with the members array", async () => {
    const user = userEvent.setup();
    render();
    await user.click(await screen.findByRole("button", { name: "Edit web" }));
    expect(await screen.findByRole("button", { name: "Add item" }, { timeout: 15000 })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Remove item 2" })).toBeTruthy();
  });

  it("creates a project with the tenant preset and locked", async () => {
    const user = userEvent.setup();
    render();
    await user.click(await screen.findByRole("button", { name: "Create project" }));
    const tenantRef = await screen.findByLabelText(/^tenantRef/, undefined, { timeout: 15000 });
    expect((tenantRef as HTMLInputElement).value).toBe("acme");
    expect((tenantRef as HTMLInputElement).readOnly).toBe(true);
  });

  it("TestProjectDeleteNeedsTypedName", async () => {
    const user = userEvent.setup();
    const { api } = render();
    await user.click(await screen.findByRole("button", { name: "Delete web" }));
    const dialog = await screen.findByRole("dialog");
    const confirm = within(dialog).getByRole("button", { name: "Delete" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    await user.type(within(dialog).getByLabelText("Name"), "web");
    await user.click(confirm);
    await waitFor(() => expect(api.calls.some((call) => call.method === "DELETE")).toBe(true));
  });
});
