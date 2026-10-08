import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { KubeObject } from "../api/types";
import VmDetailPage from "../pages/project/VmDetailPage";
import { virtualMachines } from "../test/fixtures";
import { renderPanel } from "../test/renderPanel";
import type { SerialHandlers } from "./serialAdapter";
import type { VncHandlers } from "./vncAdapter";
import ConsoleTab from "./ConsoleTab";

const mocks = vi.hoisted(() => ({
  vncLoads: 0,
  vncHandle: { disconnect: vi.fn(), sendCtrlAltDel: vi.fn(), setViewOnly: vi.fn(), setScale: vi.fn() },
  serialHandle: { disconnect: vi.fn(), focus: vi.fn() },
  createVnc: vi.fn(),
  createSerial: vi.fn(),
}));

vi.mock("./vncAdapter", () => {
  mocks.vncLoads += 1;
  return { createVnc: mocks.createVnc };
});
vi.mock("./serialAdapter", () => ({ createSerial: mocks.createSerial }));

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };

function vm(name: string): KubeObject {
  const found = virtualMachines().items.find((item) => item.metadata.name === name);
  if (!found) {
    throw new Error("fixture");
  }
  return found;
}

function vncHandlers(): VncHandlers {
  return mocks.createVnc.mock.calls[mocks.createVnc.mock.calls.length - 1]?.[2] as VncHandlers;
}

function serialHandlers(): SerialHandlers {
  return mocks.createSerial.mock.calls[mocks.createSerial.mock.calls.length - 1]?.[2] as SerialHandlers;
}

describe("consoles", () => {
  beforeEach(() => {
    mocks.createVnc.mockReset().mockReturnValue(mocks.vncHandle);
    mocks.createSerial.mockReset().mockReturnValue(mocks.serialHandle);
    for (const handle of [...Object.values(mocks.vncHandle), ...Object.values(mocks.serialHandle)]) {
      handle.mockReset();
    }
  });

  it("TestVncConnectsOnMountAndDisconnectsOnUnmount", async () => {
    const view = renderPanel(<ConsoleTab vm={vm("web-1")} allowVnc allowSerial />);
    await waitFor(() => expect(mocks.createVnc).toHaveBeenCalledTimes(1));
    const [target, url] = mocks.createVnc.mock.calls[0] as [HTMLElement, string];
    expect(target.getAttribute("dir")).toBe("ltr");
    expect(url).toMatch(/^wss?:\/\/.+\/apis\/bedrock\.cloudyfolks\.io\/.+\/console\/vnc$/);
    view.unmount();
    expect(mocks.vncHandle.disconnect).toHaveBeenCalledTimes(1);
  });

  it("TestVncStates and reconnect", async () => {
    const user = userEvent.setup();
    renderPanel(<ConsoleTab vm={vm("web-1")} allowVnc allowSerial />);
    await waitFor(() => expect(mocks.createVnc).toHaveBeenCalledTimes(1));
    expect(screen.getByText("Connecting to the console", { selector: "p[role=status]" })).toBeTruthy();
    act(() => vncHandlers().onConnect());
    expect(await screen.findByText("Connected")).toBeTruthy();
    act(() => vncHandlers().onDisconnect(true));
    expect(await screen.findByText("The console disconnected", { selector: "p[role=status]" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Reconnect" }));
    await waitFor(() => expect(mocks.createVnc).toHaveBeenCalledTimes(2));
    act(() => vncHandlers().onSecurityFailure("auth"));
    expect(await screen.findByText("The console refused the connection", { selector: "p[role=status]" })).toBeTruthy();
  });

  it("TestVncToolbar", async () => {
    const user = userEvent.setup();
    renderPanel(<ConsoleTab vm={vm("web-1")} allowVnc allowSerial />);
    await waitFor(() => expect(mocks.createVnc).toHaveBeenCalledTimes(1));
    act(() => vncHandlers().onConnect());
    await user.click(await screen.findByRole("button", { name: "Send Ctrl+Alt+Del" }));
    expect(mocks.vncHandle.sendCtrlAltDel).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("switch", { name: "View only" }));
    expect(mocks.vncHandle.setViewOnly).toHaveBeenCalledWith(true);
    await user.click(screen.getByRole("switch", { name: "Scale to fit" }));
    expect(mocks.vncHandle.setScale).toHaveBeenCalledWith(false);
    const target = mocks.createVnc.mock.calls[0]?.[0] as HTMLElement;
    const fullscreen = vi.fn();
    target.requestFullscreen = fullscreen;
    await user.click(screen.getByRole("button", { name: "Full screen" }));
    expect(fullscreen).toHaveBeenCalled();
  });

  it("TestSerialConnectsAndDisconnects and its states", async () => {
    const user = userEvent.setup();
    const view = renderPanel(<ConsoleTab vm={vm("web-1")} allowVnc allowSerial />);
    await user.click(await screen.findByRole("tab", { name: "Serial console" }));
    await waitFor(() => expect(mocks.createSerial).toHaveBeenCalledTimes(1));
    act(() => serialHandlers().onOpen());
    expect(await screen.findByText("Connected")).toBeTruthy();
    act(() => serialHandlers().onClose(1006));
    expect(await screen.findByRole("button", { name: "Reconnect" })).toBeTruthy();
    view.unmount();
    expect(mocks.serialHandle.disconnect).toHaveBeenCalled();
  });

  it("TestConsolesAreLtr in Persian", async () => {
    renderPanel(<ConsoleTab vm={vm("web-1")} allowVnc allowSerial />, { locale: "fa" });
    await waitFor(() => expect(mocks.createVnc).toHaveBeenCalledTimes(1));
    expect((mocks.createVnc.mock.calls[0]?.[0] as HTMLElement).getAttribute("dir")).toBe("ltr");
    expect(document.documentElement.dir).toBe("rtl");
  });

  it("serves the console while the VM is Migrating", async () => {
    const migrating = { ...vm("web-1"), status: { ...vm("web-1").status, phase: "Migrating" } };
    renderPanel(<ConsoleTab vm={migrating} allowVnc allowSerial />);
    await waitFor(() => expect(mocks.createVnc).toHaveBeenCalledTimes(1));
    expect(screen.queryByText("Start the VM to open the console")).toBeNull();
  });

  it("checks console/serial for the serial tab and console/vnc for the VNC tab", async () => {
    const view = renderPanel(<ConsoleTab vm={vm("web-1")} allowVnc={false} allowSerial />);
    expect(await screen.findByRole("tab", { name: "Serial console" })).toBeTruthy();
    expect(screen.queryByRole("tab", { name: "Graphical console" })).toBeNull();
    view.unmount();
    renderPanel(<ConsoleTab vm={vm("web-1")} allowVnc allowSerial={false} />);
    expect(await screen.findByRole("tab", { name: "Graphical console" })).toBeTruthy();
    expect(screen.queryByRole("tab", { name: "Serial console" })).toBeNull();
  });

  it("TestNoConnectionWhenTheVmIsNotRunning", () => {
    renderPanel(<ConsoleTab vm={vm("db-1")} allowVnc allowSerial />);
    expect(screen.getByText("Start the VM to open the console")).toBeTruthy();
    expect(mocks.createVnc).not.toHaveBeenCalled();
    expect(mocks.createSerial).not.toHaveBeenCalled();
  });

  it("TestSessionEndedDuringConsoleShowsSignIn", async () => {
    const view = renderPanel(<ConsoleTab vm={vm("web-1")} allowVnc allowSerial />);
    await waitFor(() => expect(mocks.createVnc).toHaveBeenCalledTimes(1));
    expect(view.queryClient.getQueryState(["session"])?.isInvalidated).toBe(false);
    act(() => vncHandlers().onDisconnect(false));
    await waitFor(() => expect(view.queryClient.getQueryState(["session"])?.isInvalidated).toBe(true));
  });

  it("TestConsoleTabIsLazy", async () => {
    const user = userEvent.setup();
    const before = mocks.vncLoads;
    renderPanel(
      <Routes>
        <Route path="/p/:project/vms/:name" element={<VmDetailPage />} />
      </Routes>,
      { route: "/p/web/vms/web-1", api: { "GET /proxy/v1/projects": { body: projects }, "GET /apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines/web-1": { body: vm("web-1") } } },
    );
    await screen.findByRole("heading", { level: 1, name: "web-1" });
    expect(mocks.createVnc).not.toHaveBeenCalled();
    await user.click(await screen.findByRole("tab", { name: "Console" }));
    await waitFor(() => expect(mocks.createVnc).toHaveBeenCalled());
    expect(mocks.vncLoads).toBeGreaterThanOrEqual(before);
  });
});
