import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { KubeList } from "../../api/types";
import volumes from "../../fixtures/volumes.json";
import { openApiRoutes } from "../../test/openapi";
import { renderPanel } from "../../test/renderPanel";
import { canExpand } from "./volumeHelpers";
import VolumesPage from "./VolumesPage";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const base = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/volumes";
const list = volumes as unknown as KubeList;

function render() {
  return renderPanel(
    <Routes>
      <Route path="/p/:project/volumes" element={<VolumesPage />} />
    </Routes>,
    {
      route: "/p/web/volumes",
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
