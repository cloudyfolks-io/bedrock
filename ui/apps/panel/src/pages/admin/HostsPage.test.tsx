import { act, screen, waitFor, within } from "@testing-library/react";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { KubeList } from "../../api/types";
import hosts from "../../fixtures/hosts.json";
import { createFakeSockets } from "../../test/fakeSocket";
import { renderPanel } from "../../test/renderPanel";
import HostsPage from "./HostsPage";

const hello = { type: "hello", expiresAt: "2026-10-09T00:00:00Z", limits: { maxSubscriptions: 64 } };

function render(sockets = createFakeSockets()) {
  return renderPanel(
    <Routes>
      <Route path="/admin/hosts" element={<HostsPage />} />
    </Routes>,
    { route: "/admin/hosts", socket: sockets, api: { "GET /apis/bedrock.cloudyfolks.io/v1alpha1/hosts": { body: hosts as unknown as KubeList } } },
  );
}

describe("HostsPage", () => {
  it("shows roles, readiness, maintenance and version without a create button", async () => {
    render();
    const row = (await screen.findByRole("link", { name: "host-01" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("control-plane")).toBeTruthy();
    expect(within(row).getByText("Ready")).toBeTruthy();
    expect(within(row).getByText("v1.36.0")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Create" })).toBeNull();
    expect(screen.getByText("bedrock join")).toBeTruthy();
    expect(screen.getByRole("link", { name: "host-01" }).getAttribute("href")).toBe("/admin/hosts/host-01");
  });

  it("updates conditions from the stream", async () => {
    const sockets = createFakeSockets();
    render(sockets);
    await screen.findByRole("link", { name: "host-03" });
    await waitFor(() => expect(sockets.sockets).toHaveLength(1));
    const socket = sockets.latest();
    act(() => {
      socket.open();
      socket.receive(hello);
    });
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(1));
    const host3 = (hosts as unknown as KubeList).items.find((item) => item.metadata.name === "host-03");
    if (!host3) {
      throw new Error("fixture");
    }
    const fixed = { ...host3, status: { ...host3.status, conditions: [{ type: "Ready", status: "True", reason: "Ready", message: "", lastTransitionTime: "2026-10-01T10:00:00Z" }] } };
    const row = screen.getByRole("link", { name: "host-03" }).closest("tr") as HTMLElement;
    expect(within(row).getByText("Not ready")).toBeTruthy();
    act(() => socket.receive({ type: "event", id: socket.framesOf("subscribe")[0]?.id, event: "MODIFIED", resourceVersion: "101", object: fixed }));
    await waitFor(() => expect(within(row).getByText("Ready")).toBeTruthy());
  });
});
