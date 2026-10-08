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
    expect(steps.map((step) => step.state)).toEqual(["done", "done", "done", "done", "done", "done", "active", "pending"]);
    expect(phaseSteps("Failed", "Preload").map((step) => step.state)).toEqual(["done", "done", "done", "failed", "pending", "pending", "pending", "pending"]);
  });

  it("upgradeTargets lists releases that upgrade from the running version", () => {
    expect(upgradeTargets(releaseList.items, "v0.0.9").map((item) => item.metadata.name)).toEqual(["v0.1.0"]);
    expect(upgradeTargets(releaseList.items, "v0.1.0").map((item) => item.metadata.name)).toEqual(["v0.2.0"]);
  });
});

describe("UpgradesPage", () => {
  it("TestPhaseTimeline shows text states", async () => {
    render(clusterWith({}));
    const list = await screen.findByRole("list", { name: "Upgrade phases" });
    const items = within(list).getAllByRole("listitem");
    expect(items).toHaveLength(8);
    expect(items[6]?.getAttribute("data-state")).toBe("active");
    expect(within(items[6] as HTMLElement).getByText("In progress")).toBeTruthy();
    expect(within(items[0] as HTMLElement).getByText("Done")).toBeTruthy();
    expect(within(items[7] as HTMLElement).getByText("Pending")).toBeTruthy();
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
    const { api } = render(clusterWith({ phase: "Idle" }));
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
    expect(patch?.body).toEqual({ spec: { desiredVersion: "v0.1.0" } });
  });

  it("TestUpgradeBlockedExplainsTheReason keeps start enabled", async () => {
    const list = clusterWith({ phase: "Idle", conditions: [{ type: "UpgradeBlocked", status: "True", reason: "NodeNotReady", message: "host-03 is not ready", lastTransitionTime: "2026-10-01T10:00:00Z" }] });
    render(list);
    expect(await screen.findByText("host-03 is not ready", { selector: "p" })).toBeTruthy();
    expect((screen.getByRole("button", { name: "Start upgrade" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("TestResumeAndAbortOnlyWhenFailed", async () => {
    const user = userEvent.setup();
    const running = render(clusterWith({}));
    await screen.findByRole("button", { name: "Start upgrade" });
    expect(screen.queryByRole("button", { name: "Resume" })).toBeNull();
    running.unmount();
    const { api } = render(clusterWith({ phase: "Failed", upgrade: { attempt: 1, from: "v0.0.9", to: "v0.1.0", startedAt: "2026-10-01T10:00:00Z", phaseStartedAt: "2026-10-01T10:00:00Z", failedPhase: "Workers" } }));
    await user.click(await screen.findByRole("button", { name: "Resume" }));
    await waitFor(() => expect(api.calls.some((call) => call.method === "PATCH")).toBe(true));
    expect(api.calls.find((call) => call.method === "PATCH")?.body).toEqual({ spec: { upgrade: { action: "resume" } } });
    await user.click(screen.getByRole("button", { name: "Abort" }));
    const dialog = await screen.findByRole("dialog");
    const confirm = within(dialog).getByRole("button", { name: "Abort" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    await user.type(within(dialog).getByLabelText("Name"), "abort");
    await user.click(confirm);
    await waitFor(() => expect(api.calls.filter((call) => call.method === "PATCH")).toHaveLength(2));
    expect(api.calls.filter((call) => call.method === "PATCH")[1]?.body).toEqual({ spec: { upgrade: { action: "abort" } } });
  });

  it("hides the controls without update permission", async () => {
    render(clusterWith({ phase: "Failed" }), (check) => check.verb !== "update");
    await screen.findByRole("list", { name: "Upgrade phases" });
    await waitFor(() => expect(screen.queryByRole("button", { name: "Start upgrade" })).toBeNull());
    expect(screen.queryByRole("button", { name: "Resume" })).toBeNull();
  });

  it("TestLiveClusterUpdates moves the timeline", async () => {
    const sockets = createFakeSockets();
    render(clusterWith({}), () => true, sockets);
    const list = await screen.findByRole("list", { name: "Upgrade phases" });
    await waitFor(() => expect(sockets.sockets.length).toBeGreaterThan(0));
    const socket = sockets.latest();
    act(() => {
      socket.open();
      socket.receive(hello);
    });
    await waitFor(() => expect(socket.framesOf("subscribe").length).toBeGreaterThan(0));
    const original = (clusters as unknown as KubeList).items[0] as KubeObject;
    const id = socket.framesOf("subscribe").find((frame) => frame.resource === "clusters")?.id;
    act(() => socket.receive({ type: "event", id, event: "MODIFIED", resourceVersion: "101", object: { ...original, status: { ...original.status, phase: "Verify" } } }));
    await waitFor(() => expect(within(list).getAllByRole("listitem")[7]?.getAttribute("data-state")).toBe("active"));
  });
});
