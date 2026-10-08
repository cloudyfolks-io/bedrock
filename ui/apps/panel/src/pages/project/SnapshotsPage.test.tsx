import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { KubeList } from "../../api/types";
import snapshots from "../../fixtures/volumesnapshots.json";
import { renderPanel } from "../../test/renderPanel";
import SnapshotsPage, { volumeFromSnapshot } from "./SnapshotsPage";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const base = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web";
const list = snapshots as unknown as KubeList;

function render() {
  return renderPanel(
    <Routes>
      <Route path="/p/:project/snapshots" element={<SnapshotsPage />} />
    </Routes>,
    { route: "/p/web/snapshots", api: { "GET /proxy/v1/projects": { body: projects }, [`GET ${base}/volumesnapshots`]: { body: list }, [`POST ${base}/volumes`]: { status: 201, body: {} }, [`DELETE ${base}/volumesnapshots/data-1-snap`]: { body: {} } } },
  );
}

describe("SnapshotsPage", () => {
  it("lists the volume, phase and retention", async () => {
    render();
    const row = (await screen.findByRole("link", { name: "data-1-snap" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("data-1")).toBeTruthy();
    expect(within(row).getByText("Ready")).toBeTruthy();
    expect(within(row).getByText("168h")).toBeTruthy();
  });

  it("builds a volume from a snapshot", () => {
    expect(volumeFromSnapshot(list.items[0] as never, "restored", 100)).toEqual({
      apiVersion: "bedrock.cloudyfolks.io/v1alpha1",
      kind: "Volume",
      metadata: { name: "restored", namespace: "acme-web" },
      spec: { sizeGi: 100, source: { snapshotRef: "data-1-snap" } },
    });
  });

  it("restores to a new volume with snapshotRef", async () => {
    const user = userEvent.setup();
    const { api } = render();
    await user.click(await screen.findByRole("button", { name: "Actions for data-1-snap" }));
    await user.click(await screen.findByRole("menuitem", { name: "Restore to a new volume" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("Name"), "restored");
    await user.click(within(dialog).getByRole("button", { name: "Create" }));
    await waitFor(() => expect(api.calls.some((call) => call.method === "POST" && call.path !== "/proxy/v1/authz")).toBe(true));
    expect(api.calls.find((call) => call.method === "POST" && call.path !== "/proxy/v1/authz")?.body).toMatchObject({ metadata: { name: "restored" }, spec: { sizeGi: 100, source: { snapshotRef: "data-1-snap" } } });
  });

  it("deletes with one confirmation", async () => {
    const user = userEvent.setup();
    const { api } = render();
    await user.click(await screen.findByRole("button", { name: "Actions for data-1-snap" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete" }));
    await user.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(api.calls.some((call) => call.method === "DELETE")).toBe(true));
  });
});
