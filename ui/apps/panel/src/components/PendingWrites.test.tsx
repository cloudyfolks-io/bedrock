import { act, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { expect, it } from "vitest";
import { useHttp } from "../api/HttpProvider";
import { configureWrites, trackWrite } from "../api/pendingWrites";
import { ResourceDetailPage } from "../resources/ResourceDetailPage";
import { ResourceListPage } from "../resources/ResourceListPage";
import { definitionFor, refOf } from "../resources/registry";
import { virtualMachines } from "../test/fixtures";
import { renderPanel } from "../test/renderPanel";
import { PendingWrites } from "./PendingWrites";

it("shows in-flight writes on the matching row and detail without changing server status", async () => {
  const def = definitionFor("virtualmachines");
  const list = virtualMachines();
  const object = list.items.find((item) => item.metadata.name === "web-1");
  const deleting = list.items.find((item) => item.metadata.name === "old");
  if (!def || !object || !deleting) throw new Error("missing virtual machine fixture");
  deleting.metadata.deletionTimestamp = "2026-10-09T00:00:00Z";
  const ref = refOf(def);
  const path = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines";
  let finish!: () => void;
  const request = new Promise<void>((resolve) => { finish = resolve; });

  function StartWrites() {
    const http = useHttp();
    return <button onClick={() => {
      configureWrites(http, () => Promise.resolve());
      void trackWrite(http, { kind: "action", ref, namespace: "acme-web", name: "web-1", action: "stop" }, () => request);
      void trackWrite(http, { kind: "create", ref, namespace: "acme-web", name: "new-vm" }, () => request);
      void trackWrite(http, { kind: "delete", ref, namespace: "other-project", name: "db-1" }, () => request);
    }}>Start writes</button>;
  }

  renderPanel(
    <Routes>
      <Route path="/p/:project/vms/:name" element={<>
        <StartWrites /><PendingWrites /><ResourceListPage def={def} /><ResourceDetailPage def={def} />
      </>} />
    </Routes>,
    {
      route: "/p/web/vms/web-1",
      api: {
        "GET /proxy/v1/projects": { body: { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] } },
        [`GET ${path}`]: { body: list },
        [`GET ${path}/web-1`]: { body: object },
      },
    },
  );

  const row = (await screen.findByRole("link", { name: "web-1" })).closest("tr")!;
  const dbRow = screen.getByRole("link", { name: "db-1" }).closest("tr")!;
  const deletingRow = screen.getByRole("link", { name: "old" }).closest("tr")!;
  const detailStatus = await screen.findByRole("status", { name: "Status" });
  await screen.findByRole("button", { name: "Stop" });
  await within(row).findByRole("button", { name: "Actions for web-1" });
  expect(within(deletingRow).getByText("Deleting")).toBeTruthy();
  expect(within(deletingRow).queryByRole("button")).toBeNull();

  await userEvent.setup().click(screen.getByRole("button", { name: "Start writes" }));
  expect(within(row).getByText("Requesting action")).toBeTruthy();
  expect(within(detailStatus).getByText("Requesting action")).toBeTruthy();
  expect(within(row).queryByRole("button")).toBeNull();
  expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
  expect(within(row).getByText("Running")).toBeTruthy();
  expect(within(detailStatus).getByText("Running")).toBeTruthy();
  expect(screen.getByText("Changes in progress").closest('[role="status"]')).toBeTruthy();
  expect(within(dbRow).queryByText("Deleting")).toBeNull();
  const provisionalRow = screen.getByText("Creating").closest("tr")!;
  expect(provisionalRow.textContent).toContain("new-vm");
  expect(within(provisionalRow).queryByRole("link")).toBeNull();
  expect(within(provisionalRow).queryByRole("button")).toBeNull();

  await act(async () => finish());
  expect(screen.queryByText("Creating")).toBeNull();
  expect(screen.queryByText("Requesting action")).toBeNull();
  expect(screen.queryByText("Changes in progress")).toBeNull();
  expect(screen.getByRole("button", { name: "Stop" })).toBeTruthy();
});
