import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { Check } from "../api/types";
import { createFakeSockets } from "../test/fakeSocket";
import { virtualMachines } from "../test/fixtures";
import { renderPanel } from "../test/renderPanel";
import { definitionFor } from "./registry";
import { ResourceDetailPage } from "./ResourceDetailPage";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const vmPath = "GET /apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines/web-1";
const hello = { type: "hello", expiresAt: "2026-10-09T00:00:00Z", limits: { maxSubscriptions: 64 } };

function web1() {
  const found = virtualMachines().items.find((item) => item.metadata.name === "web-1");
  if (!found) {
    throw new Error("fixture");
  }
  return found;
}

function renderDetail(options: { allow?: (check: Check) => boolean; sockets?: ReturnType<typeof createFakeSockets>; body?: unknown; status?: number } = {}) {
  const def = definitionFor("virtualmachines");
  if (!def) {
    throw new Error("missing definition");
  }
  return renderPanel(
    <Routes>
      <Route path="/p/:project/vms/:name" element={<ResourceDetailPage def={def} />} />
    </Routes>,
    {
      route: "/p/web/vms/web-1",
      ...(options.allow ? { allow: options.allow } : {}),
      ...(options.sockets ? { socket: options.sockets } : {}),
      api: { "GET /proxy/v1/projects": { body: projects }, [vmPath]: { status: options.status ?? 200, body: options.body ?? web1() } },
    },
  );
}

describe("ResourceDetailPage", () => {
  it("shows the title, conditions, and the spec as left to right JSON", async () => {
    renderDetail();
    expect(await screen.findByRole("heading", { level: 1, name: "web-1" })).toBeTruthy();
    expect(screen.getByRole("table", { name: "Conditions" })).toBeTruthy();
    const pre = document.querySelector("pre");
    expect(pre?.getAttribute("dir")).toBe("ltr");
    expect(pre?.textContent).toContain('"instanceType": "u1.medium"');
  });

  it("updates from the stream and shows a deleted object", async () => {
    const sockets = createFakeSockets();
    renderDetail({ sockets });
    await screen.findByRole("heading", { level: 1, name: "web-1" });
    await waitFor(() => expect(sockets.sockets).toHaveLength(1));
    const socket = sockets.latest();
    act(() => {
      socket.open();
      socket.receive(hello);
    });
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(1));
    const id = socket.framesOf("subscribe")[0]?.id;
    expect(screen.getAllByText("Running").length).toBeGreaterThan(0);
    act(() => socket.receive({ type: "event", id, event: "MODIFIED", resourceVersion: "101", object: { ...web1(), status: { ...web1().status, phase: "Stopping" } } }));
    expect(await screen.findByText("Stopping")).toBeTruthy();
    act(() => socket.receive({ type: "event", id, event: "DELETED", resourceVersion: "102", object: web1() }));
    expect(await screen.findByText("This object was deleted")).toBeTruthy();
  });

  it("shows action buttons that follow the checks with the object name", async () => {
    const user = userEvent.setup();
    const { api } = renderDetail({ allow: (check) => check.subresource === "stop" && check.name === "web-1" });
    await screen.findByRole("heading", { level: 1, name: "web-1" });
    expect(await screen.findByRole("button", { name: "Stop" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Start" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
    const authz = api.calls.filter((call) => call.path === "/proxy/v1/authz");
    expect(authz).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Stop" }));
    expect(await screen.findByRole("alertdialog")).toBeTruthy();
  });

  it("shows no access for a forbidden object", async () => {
    renderDetail({ status: 403, body: { error: "forbidden" } });
    expect(await screen.findByText("You cannot view this")).toBeTruthy();
  });
});
