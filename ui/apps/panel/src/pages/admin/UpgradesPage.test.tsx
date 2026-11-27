import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { Check, KubeList, KubeObject } from "../../api/types";
import clusters from "../../fixtures/clusters.json";
import releases from "../../fixtures/releases.json";
import { createFakeSockets } from "../../test/fakeSocket";
import { renderPanel } from "../../test/renderPanel";
import UpgradesPage from "./UpgradesPage";
import { phaseSteps, upgradeTargets } from "./upgradeHelpers";

const base = "/apis/bedrock.cloudyfolks.io/v1alpha1";
const hello = { type: "hello", expiresAt: "2026-10-09T00:00:00Z", limits: { maxSubscriptions: 64 } };
const releaseList = releases as unknown as KubeList;
const upgrade = {
  attempt: 1,
  from: "v0.0.9",
  to: "v0.1.0",
  startedAt: "2026-10-01T10:00:00Z",
  phaseStartedAt: "2026-10-01T10:01:00Z",
};

function clusterWith(status: Record<string, unknown>, spec: Record<string, unknown> = {}): KubeList {
  const original = (clusters as unknown as KubeList).items[0] as KubeObject;
  return { ...(clusters as unknown as KubeList), items: [{ ...original, spec: { ...original.spec, ...spec }, status: { ...original.status, ...status } }] };
}

function render(list: KubeList, allow: (check: Check) => boolean = () => true, sockets = createFakeSockets()) {
  return renderPanel(
    <Routes>
      <Route path="/admin/upgrades" element={<UpgradesPage />} />
    </Routes>,
    {
      route: "/admin/upgrades",
      allow,
      socket: sockets,
      api: { [`GET ${base}/clusters`]: { body: list }, [`GET ${base}/releases`]: { body: releaseList }, [`PATCH ${base}/clusters/cluster`]: { body: {} } },
    },
  );
}

describe("helpers", () => {
  it("phaseSteps marks done, active and pending", () => {
    const steps = phaseSteps("Workers");
    expect(steps.map((step) => step.state)).toEqual(["done", "done", "done", "done", "done", "active", "pending"]);
    expect(phaseSteps("Failed", "Preload").map((step) => step.state)).toEqual(["done", "done", "failed", "pending", "pending", "pending", "pending"]);
    expect(phaseSteps("Idle").every((step) => step.state === "pending")).toBe(true);
  });

  it("upgradeTargets lists releases that upgrade from the running version", () => {
    expect(upgradeTargets(releaseList.items, "v0.0.9").map((item) => item.metadata.name)).toEqual(["v0.1.0"]);
    expect(upgradeTargets(releaseList.items, "v0.1.0").map((item) => item.metadata.name)).toEqual(["v0.2.0"]);
  });
});

describe("UpgradesPage", () => {
  it("leads with versions and the active step before the ordered phases", async () => {
    render(clusterWith({}));
    const status = await screen.findByRole("region", { name: "Upgrade status" });
    const list = await screen.findByRole("list", { name: "Upgrade phases" });
    expect(status.compareDocumentPosition(list) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(within(status).getByText("v0.0.9")).toBeTruthy();
    expect(within(status).getByText("v0.1.0")).toBeTruthy();
    expect(within(status).getByText("Workers")).toBeTruthy();
    expect(within(status).getByText("In progress")).toBeTruthy();
    expect(within(status).getByText("workers are upgrading")).toBeTruthy();
    const items = within(list).getAllByRole("listitem");
    expect(items).toHaveLength(7);
    expect(within(list).queryByText("Idle")).toBeNull();
    expect(items[5]?.getAttribute("aria-current")).toBe("step");
    expect(within(items[5] as HTMLElement).getByText("In progress")).toBeTruthy();
    expect(within(items[0] as HTMLElement).getByText("Done")).toBeTruthy();
    expect(within(items[6] as HTMLElement).getByText("Pending")).toBeTruthy();
  });

  it("TestConditionsAndComponents", async () => {
    render(clusterWith({}));
    await screen.findByRole("table", { name: "Conditions" });
    for (const type of ["Available", "Progressing", "Degraded", "UpgradeBlocked"]) {
      expect(screen.getAllByText(type).length).toBeGreaterThan(0);
    }
    expect(screen.getByText("kube-ovn")).toBeTruthy();
    expect(screen.getByText("s3://backups/etcd")).toBeTruthy();
    expect(screen.getByText("sha256:0123456789abcdef")).toBeTruthy();
  });

  it("TestUpgradeTargets starts an upgrade with a merge patch", async () => {
    const user = userEvent.setup();
    const { api } = render(clusterWith({ phase: "Idle", upgrade: null }, { desiredVersion: "v0.0.9" }));
    await user.click(await screen.findByRole("combobox", { name: /Target version/ }));
    const options = await screen.findAllByRole("option");
    expect(options.map((option) => option.textContent)).toEqual(["v0.1.0"]);
    await user.click(options[0] as HTMLElement);
    await user.click(screen.getByRole("button", { name: "Start upgrade" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Start upgrade" }));
    await waitFor(() => expect(api.calls.some((call) => call.method === "PATCH")).toBe(true));
    const patch = api.calls.find((call) => call.method === "PATCH");
    expect(patch?.path).toBe(`${base}/clusters/cluster`);
    expect(patch?.body).toEqual({ metadata: { resourceVersion: "100" }, spec: { desiredVersion: "v0.1.0" } });
  });

  it("shows the blocker and can retry the same target", async () => {
    const user = userEvent.setup();
    const list = clusterWith({ phase: "Idle", upgrade: null, conditions: [{ type: "UpgradeBlocked", status: "True", reason: "NodeNotReady", message: "host-03 is not ready", lastTransitionTime: "2026-10-01T10:00:00Z" }] });
    const { api } = render(list);
    const status = within(await screen.findByRole("region", { name: "Upgrade status" }));
    expect(status.getByText("Upgrade blocked")).toBeTruthy();
    expect(status.getByText("NodeNotReady")).toBeTruthy();
    expect(status.getByText("host-03 is not ready")).toBeTruthy();
    await user.click(screen.getByRole("combobox", { name: /Target version/ }));
    await user.click(await screen.findByRole("option", { name: "v0.1.0" }));
    await user.click(screen.getByRole("button", { name: "Start upgrade" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Start upgrade" }));
    await waitFor(() => expect(api.calls.find((call) => call.method === "PATCH")?.body).toEqual({
      metadata: { resourceVersion: "100" }, spec: { upgrade: { action: "resume" } },
    }));
  });

  it.each([
    ["Idle", null, {}, "Upgrade requested."],
    ["Preload", { ...upgrade, message: "preload 0/1 nodes, waiting for host-03" }, {}, "preload 0/1 nodes, waiting for host-03"],
    ["Failed", { ...upgrade, failedPhase: "Preload" }, { upgrade: { action: "abort" } }, "Rollback requested."],
  ] as const)("shows status and suppresses actions during %s", async (phase, operation, spec, message) => {
    render(clusterWith({ phase, upgrade: operation }, spec));
    expect(await screen.findByText(message, { selector: 'p[role="status"]' })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Start upgrade" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Resume" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Abort" })).toBeNull();
  });

  it("can resume or abort a failed early phase", async () => {
    const user = userEvent.setup();
    const { api } = render(clusterWith({ phase: "Failed", upgrade: { ...upgrade, failedPhase: "Preload", message: "Image import failed" } }));
    await user.click(await screen.findByRole("button", { name: "Resume" }));
    await waitFor(() => expect(api.calls.some((call) => call.method === "PATCH")).toBe(true));
    expect(api.calls.find((call) => call.method === "PATCH")?.body).toEqual({ metadata: { resourceVersion: "100" }, spec: { upgrade: { action: "resume" } } });
    const abort = screen.getByRole("button", { name: "Abort" }) as HTMLButtonElement;
    await waitFor(() => expect(abort.disabled).toBe(false));
    await user.click(abort);
    const dialog = await screen.findByRole("dialog");
    const confirm = within(dialog).getByRole("button", { name: "Abort" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    await user.type(within(dialog).getByLabelText("Name"), "abort");
    await user.click(confirm);
    await waitFor(() => expect(api.calls.filter((call) => call.method === "PATCH")).toHaveLength(2));
    expect(api.calls.filter((call) => call.method === "PATCH")[1]?.body).toEqual({ metadata: { resourceVersion: "100" }, spec: { upgrade: { action: "abort" } } });
    expect(screen.queryByRole("button", { name: "Start upgrade" })).toBeNull();
  });

  it("offers only resume after Components starts", async () => {
    render(clusterWith({ phase: "Failed", upgrade: { ...upgrade, failedPhase: "Workers" } }));
    expect(await screen.findByRole("button", { name: "Resume" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Abort" })).toBeNull();
  });

  it("hides the controls without update permission", async () => {
    render(clusterWith({ phase: "Failed" }), (check) => check.verb !== "patch" && check.verb !== "update");
    await screen.findByRole("list", { name: "Upgrade phases" });
    await waitFor(() => expect(screen.queryByRole("button", { name: "Start upgrade" })).toBeNull());
    expect(screen.queryByRole("button", { name: "Resume" })).toBeNull();
  });

  it("updates phase details and cancels a stale start confirmation", async () => {
    const user = userEvent.setup();
    const sockets = createFakeSockets();
    const { api } = render(clusterWith({ phase: "Idle", upgrade: null }, { desiredVersion: "v0.0.9" }), () => true, sockets);
    const list = await screen.findByRole("list", { name: "Upgrade phases" });
    await user.click(await screen.findByRole("combobox", { name: /Target version/ }));
    await user.click(await screen.findByRole("option", { name: "v0.1.0" }));
    await user.click(screen.getByRole("button", { name: "Start upgrade" }));
    expect(await screen.findByRole("alertdialog")).toBeTruthy();
    await waitFor(() => expect(sockets.sockets.length).toBeGreaterThan(0));
    const socket = sockets.latest();
    act(() => {
      socket.open();
      socket.receive(hello);
    });
    await waitFor(() => expect(socket.framesOf("subscribe").length).toBeGreaterThan(0));
    const original = (clusters as unknown as KubeList).items[0] as KubeObject;
    const id = socket.framesOf("subscribe").find((frame) => frame.resource === "clusters")?.id;
    act(() => socket.receive({
      type: "event", id, event: "MODIFIED", resourceVersion: "101",
      object: {
        ...original,
        metadata: { ...original.metadata, resourceVersion: "101" },
        status: { ...original.status, phase: "Preload", upgrade: { ...upgrade, message: "Waiting for host-03" } },
      },
    }));
    expect(await screen.findByText("Waiting for host-03")).toBeTruthy();
    expect(within(list).getAllByRole("listitem")[2]?.getAttribute("data-state")).toBe("active");
    expect(screen.getByText("Upgrade start (UTC)").nextElementSibling?.querySelector("time")?.dateTime).toBe(upgrade.startedAt);
    expect(screen.getByText("Phase start (UTC)").nextElementSibling?.querySelector("time")?.dateTime).toBe(upgrade.phaseStartedAt);
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(screen.queryByRole("button", { name: "Start upgrade" })).toBeNull();
    expect(api.calls.some((call) => call.method === "PATCH")).toBe(false);
    act(() => socket.receive({
      type: "event", id, event: "MODIFIED", resourceVersion: "102",
      object: {
        ...original,
        metadata: { ...original.metadata, resourceVersion: "102" },
        status: { ...original.status, phase: "Idle", version: "v0.1.0", upgrade: null },
      },
    }));
    expect(await screen.findByRole("button", { name: "Start upgrade" })).toBeTruthy();
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});
