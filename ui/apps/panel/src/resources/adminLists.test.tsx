import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { KubeList } from "../api/types";
import clusterImages from "../fixtures/clusterimages.json";
import tenants from "../fixtures/tenants.json";
import { renderPanel } from "../test/renderPanel";
import { definitionFor } from "./registry";
import { ResourceListPage } from "./ResourceListPage";

const base = "/apis/bedrock.cloudyfolks.io/v1alpha1";

function render(plural: string, path: string, body: unknown) {
  const def = definitionFor(plural);
  if (!def) {
    throw new Error("missing definition");
  }
  return renderPanel(
    <Routes>
      <Route path={`/admin/${def.path}`} element={<ResourceListPage def={def} />} />
    </Routes>,
    { route: `/admin/${def.path}`, api: { [`GET ${base}/${plural}`]: { body }, [`DELETE ${base}/${plural}/${path}`]: { body: {} } } },
  );
}

describe("admin lists", () => {
  it("TestTenantsList shows quota and CIDR in left to right text", async () => {
    const { container } = render("tenants", "acme", tenants as unknown as KubeList);
    const row = (await screen.findByRole("link", { name: "acme" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("Acme Corp")).toBeTruthy();
    expect(row.querySelector('bdi[dir="ltr"]')?.textContent).toBe("10.100.0.0/16");
    expect(container.querySelectorAll("tbody tr")).toHaveLength(2);
  });

  it("TestTenantDeleteNeedsTypedName", async () => {
    const user = userEvent.setup();
    const { api } = render("tenants", "acme", tenants as unknown as KubeList);
    await user.click(await screen.findByRole("button", { name: "Actions for acme" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog");
    const confirm = within(dialog).getByRole("button", { name: "Delete" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    await user.type(within(dialog).getByLabelText("Name"), "acme");
    await user.click(confirm);
    await waitFor(() => expect(api.calls.some((call) => call.method === "DELETE")).toBe(true));
  });

  it("TestIsoAndDiskTypesAreBadged and importing shows text", async () => {
    render("clusterimages", "ubuntu-24-04", clusterImages as unknown as KubeList);
    const disk = (await screen.findByRole("link", { name: "ubuntu-24-04" })).closest("tr") as HTMLElement;
    expect(disk.querySelector('.br-badge[data-tone="info"]')?.textContent).toBe("disk");
    const iso = screen.getByRole("link", { name: "virtio-win" }).closest("tr") as HTMLElement;
    expect(within(iso).getByText("iso")).toBeTruthy();
    expect(within(iso).getByText("Importing")).toBeTruthy();
  });
});
