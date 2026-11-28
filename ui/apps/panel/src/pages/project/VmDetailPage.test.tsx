import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { Check, KubeObject } from "../../api/types";
import { createFakeSockets } from "../../test/fakeSocket";
import { virtualMachines } from "../../test/fixtures";
import { renderPanel } from "../../test/renderPanel";
import VmDetailPage from "./VmDetailPage";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const base = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines";
const hello = { type: "hello", expiresAt: "2026-10-09T00:00:00Z", limits: { maxSubscriptions: 64 } };

function vm(name: string): KubeObject {
  const found = virtualMachines().items.find((item) => item.metadata.name === name);
  if (!found) {
    throw new Error("fixture");
  }
  return found;
}

function render(name: string, route: string, sockets = createFakeSockets(), body: KubeObject = vm(name), allow?: (check: Check) => boolean) {
  return renderPanel(
    <Routes>
      <Route path="/p/:project/vms/:name" element={<VmDetailPage />} />
    </Routes>,
    { route, socket: sockets, ...(allow ? { allow } : {}), api: { "GET /proxy/v1/projects": { body: projects }, [`GET ${base}/${name}`]: { body } } },
  );
}

describe("VmDetailPage", () => {
  it("places live health beside the title and links overview facts to hardware", async () => {
    const user = userEvent.setup();
    render("web-1", "/p/web/vms/web-1");
    const title = await screen.findByRole("heading", { level: 1, name: "web-1" });
    expect(within(title.closest("header") as HTMLElement).getByRole("status").textContent).toContain("Running");
    const hardware = within(screen.getByRole("region", { name: "Hardware" }));
    expect(hardware.getByText("u1.medium")).toBeTruthy();
    expect(hardware.getByText("ubuntu-24-04")).toBeTruthy();
    expect(hardware.getByText("host-01")).toBeTruthy();
    const connection = within(screen.getByRole("region", { name: "Connection" }));
    expect(connection.getByText("10.100.0.5").getAttribute("dir")).toBe("ltr");
    expect(connection.getByText("Ubuntu 24.04")).toBeTruthy();
    expect(connection.getByText("web-ip")).toBeTruthy();
    await user.click(hardware.getByRole("link", { name: "Disks" }));
    expect(await screen.findByRole("tab", { name: "Disks", selected: true })).toBeTruthy();
  });

  it("follows the tab parameter and shows disks and networks", async () => {
    const user = userEvent.setup();
    render("web-1", "/p/web/vms/web-1?tab=disks");
    const volume = await screen.findByRole("link", { name: "data-1" });
    expect(volume.getAttribute("href")).toBe("/p/web/volumes/data-1");
    expect(screen.getByText("40 GiB")).toBeTruthy();
    const tabs = within(screen.getByRole("tablist", { name: "Virtual machines" }));
    tabs.getByRole("tab", { name: "Disks", selected: true }).focus();
    await user.keyboard("{ArrowRight}");
    expect(document.activeElement).toBe(tabs.getByRole("tab", { name: "Networks" }));
    await user.keyboard("{Enter}");
    const network = within(await screen.findByRole("region", { name: "Network attachments" }));
    expect(network.getByText("52:54:00:12:34:56").getAttribute("dir")).toBe("ltr");
    expect(network.getByText("web")).toBeTruthy();
  });

  it("links a preexisting root volume instead of guessing its size", async () => {
    const original = vm("db-1");
    render("db-1", "/p/web/vms/db-1?tab=disks", undefined, { ...original, spec: { ...original.spec, rootDisk: { volumeRef: "root" } } });
    expect((await screen.findByRole("link", { name: "root" })).getAttribute("href")).toBe("/p/web/volumes/root");
    expect(screen.queryByText("GiB")).toBeNull();
  });

  it("explains that the console needs a running machine", async () => {
    render("db-1", "/p/web/vms/db-1?tab=console");
    expect(await screen.findByText("Start the VM to open the console")).toBeTruthy();
  });

  it("falls back to Overview on a denied console deep link", async () => {
    const { api } = render("web-1", "/p/web/vms/web-1?tab=console", undefined, vm("web-1"), () => false);
    expect(await screen.findByRole("tab", { name: "Overview", selected: true })).toBeTruthy();
    await waitFor(() => expect(api.calls.some((call) => call.path === "/proxy/v1/authz")).toBe(true));
    expect(screen.queryByRole("tab", { name: "Console" })).toBeNull();
    expect(screen.queryByText("Start the VM to open the console")).toBeNull();
  });

  it("opens the lazy console and metrics tabs", async () => {
    const user = userEvent.setup();
    const { api } = render("web-1", "/p/web/vms/web-1");
    await user.click(await screen.findByRole("tab", { name: "Metrics" }));
    expect(await screen.findByRole("heading", { level: 2, name: "CPU use" })).toBeTruthy();
    await waitFor(() => expect(api.calls.some((call) => call.path === "/proxy/v1/metrics/range" && call.query.get("query") === "vm.cpu" && call.query.get("namespace") === "acme-web")).toBe(true));
  });

  it("shows the deleted state and live status", async () => {
    const sockets = createFakeSockets();
    render("web-1", "/p/web/vms/web-1", sockets);
    await screen.findByRole("heading", { level: 1, name: "web-1" });
    await waitFor(() => expect(sockets.sockets.length).toBeGreaterThan(0));
    const socket = sockets.latest();
    act(() => {
      socket.open();
      socket.receive(hello);
    });
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(1));
    const id = socket.framesOf("subscribe")[0]?.id;
    const original = vm("web-1");
    act(() => socket.receive({ type: "event", id, event: "MODIFIED", resourceVersion: "101", object: { ...original, status: { ...original.status, phase: "Stopping" } } }));
    expect((await screen.findAllByText("Stopping")).length).toBeGreaterThan(0);
    act(() => socket.receive({ type: "event", id, event: "DELETED", resourceVersion: "102", object: original }));
    expect(await screen.findByText("This object was deleted")).toBeTruthy();
  });
});
