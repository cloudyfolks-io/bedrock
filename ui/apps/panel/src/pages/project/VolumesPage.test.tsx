import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { Check, KubeList } from "../../api/types";
import volumes from "../../fixtures/volumes.json";
import { openApiRoutes } from "../../test/openapi";
import { renderPanel } from "../../test/renderPanel";
import { canExpand } from "./volumeHelpers";
import VolumeDetailPage from "./VolumeDetailPage";
import VolumesPage from "./VolumesPage";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const base = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/volumes";
const list = volumes as unknown as KubeList;

function render(route = "/p/web/volumes", allow = (_check: Check) => true) {
  return renderPanel(
    <Routes>
      <Route path="/p/:project/volumes" element={<VolumesPage />} />
      <Route path="/p/:project/volumes/:name" element={<VolumeDetailPage />} />
    </Routes>,
    {
      route,
      allow,
      api: { ...openApiRoutes(), "GET /proxy/v1/projects": { body: projects }, [`GET ${base}`]: { body: list }, [`GET ${base}/data-1`]: { body: list.items[0] }, [`PATCH ${base}/data-1`]: { body: {} }, [`DELETE ${base}/data-1`]: { body: {} } },
    },
  );
}

describe("canExpand", () => {
  it("only grows", () => {
    const volume = { metadata: { name: "v" }, spec: { sizeGi: 100 } };
    expect(canExpand(volume, 101)).toBe(true);
    expect(canExpand(volume, 100)).toBe(false);
    expect(canExpand(volume, 50)).toBe(false);
    expect(canExpand(volume, 120.5)).toBe(false);
  });
});

describe("VolumeDetailPage", () => {
  it("shows the capacity, attachment, metric and Expand action above the technical details", async () => {
    const user = userEvent.setup();
    render("/p/web/volumes/data-1");
    const expand = await screen.findByRole("button", { name: "Expand" });
    const overview = within(screen.getByRole("region", { name: "Overview" }));
    expect(overview.getByText("100 GiB")).toBeTruthy();
    expect(overview.getByRole("link", { name: "web-1" }).getAttribute("href")).toBe("/p/web/vms/web-1");
    const chart = screen.getByRole("heading", { name: "Volume use" });
    const specification = screen.getByText("Specification").closest("details")!;
    expect(specification.open).toBe(false);
    expect(chart.compareDocumentPosition(specification) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    await user.click(expand);
    expect(await screen.findByRole("dialog", { name: "Expand data-1" })).toBeTruthy();
  });

  it("does not offer Expand without volume update permission", async () => {
    render("/p/web/volumes/data-1", (check) => check.verb !== "update");
    expect(await screen.findByRole("button", { name: "Delete" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Expand" })).toBeNull();
  });
});

describe("VolumesPage", () => {
  it("shows phase, size, class and links the attached machine", async () => {
    render();
    const row = (await screen.findByRole("link", { name: "data-1" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("In use")).toBeTruthy();
    expect(within(row).getByText("100 GiB")).toBeTruthy();
    expect(within(row).getByText("block")).toBeTruthy();
    expect(within(row).getByRole("link", { name: "web-1" }).getAttribute("href")).toBe("/p/web/vms/web-1");
    const scratch = screen.getByRole("link", { name: "scratch" }).closest("tr") as HTMLElement;
    expect(within(scratch).getByText("snapshot data-1-snap")).toBeTruthy();
  });

  it("TestExpandOnlyGrows", async () => {
    const user = userEvent.setup();
    const { api } = render();
    await user.click(await screen.findByRole("button", { name: "Actions for data-1" }));
    await user.click(await screen.findByRole("menuitem", { name: "Expand" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("100 GiB")).toBeTruthy();
    expect(within(dialog).getByText("web-1")).toBeTruthy();
    const size = within(dialog).getByRole("textbox", { name: "New size (GiB)" });
    const confirm = within(dialog).getByRole("button", { name: "Expand" }) as HTMLButtonElement;
    await user.clear(size);
    await user.type(size, "100");
    expect(confirm.disabled).toBe(true);
    await user.clear(size);
    await user.type(size, "150");
    expect(confirm.disabled).toBe(false);
    await user.click(confirm);
    await waitFor(() => expect(api.calls.some((call) => call.method === "PATCH")).toBe(true));
    expect(api.calls.find((call) => call.method === "PATCH")?.body).toEqual({ spec: { sizeGi: 150 } });
  });

  it("makes the storage class read only on edit", async () => {
    const user = userEvent.setup();
    render();
    await user.click(await screen.findByRole("button", { name: "Actions for data-1" }));
    await user.click(await screen.findByRole("menuitem", { name: "Edit" }));
    const input = await screen.findByLabelText(/^storageClass/, undefined, { timeout: 15000 });
    expect((input as HTMLInputElement).readOnly).toBe(true);
  });

  it("asks for the typed name before delete", async () => {
    const user = userEvent.setup();
    const { api } = render();
    await user.click(await screen.findByRole("button", { name: "Actions for data-1" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("Name"), "data-1");
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(api.calls.some((call) => call.method === "DELETE")).toBe(true));
  });
});
