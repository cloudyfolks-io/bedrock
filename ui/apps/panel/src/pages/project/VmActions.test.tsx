import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { Check, KubeObject } from "../../api/types";
import { virtualMachines } from "../../test/fixtures";
import type { FakeRoute } from "../../test/fakeApi";
import { renderPanel } from "../../test/renderPanel";
import { actionStates } from "../../resources/vmActionStates";
import VmDetailPage from "./VmDetailPage";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const base = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines";

function vm(name: string): KubeObject {
  const found = virtualMachines().items.find((item) => item.metadata.name === name);
  if (!found) {
    throw new Error("fixture");
  }
  return found;
}

function render(name: string, allow: (check: Check) => boolean = () => true, extra: Record<string, FakeRoute> = {}, route = `/p/web/vms/${name}`) {
  return renderPanel(
    <Routes>
      <Route path="/p/:project/vms/:name" element={<VmDetailPage />} />
    </Routes>,
    { route, allow, api: { "GET /proxy/v1/projects": { body: projects }, [`GET ${base}/${name}`]: { body: vm(name) }, ...extra } },
  );
}

describe("actionStates", () => {
  it("TestActionStatesByPhase", () => {
    const states = (phase: string) => actionStates({ metadata: { name: "x" }, status: { phase } });
    expect(states("Stopped")).toEqual({ start: "enabled", stop: "disabled", restart: "disabled", migrate: "disabled", snapshot: "enabled", delete: "enabled" });
    expect(states("Running")).toEqual({ start: "disabled", stop: "enabled", restart: "enabled", migrate: "enabled", snapshot: "enabled", delete: "enabled" });
    for (const phase of ["Starting", "Stopping"]) {
      expect(states(phase)).toEqual({ start: "disabled", stop: "disabled", restart: "disabled", migrate: "disabled", snapshot: "disabled", delete: "enabled" });
    }
    expect(Object.values(states("Migrating")).every((state) => state === "disabled")).toBe(true);
    expect(states("Failed")).toEqual({ start: "enabled", stop: "disabled", restart: "disabled", migrate: "disabled", snapshot: "disabled", delete: "enabled" });
  });
});

describe("VmActions", () => {
  it("TestEachActionPostsTheSubresource", async () => {
    const user = userEvent.setup();
    const { api } = render("web-1", () => true, { [`POST ${base}/web-1/*`]: { body: { message: "snapshot started" } } });
    await user.click(await screen.findByRole("button", { name: "Snapshot" }));
    expect(await screen.findByText("snapshot started")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Migrate" }));
    await user.click(screen.getByRole("button", { name: "Stop" }));
    await user.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Stop" }));
    await waitFor(() => expect(api.calls.filter((call) => call.method === "POST" && call.path.includes("/virtualmachines/web-1/")).length).toBe(3));
    const posts = api.calls.filter((call) => call.method === "POST" && call.path.includes("/virtualmachines/web-1/"));
    expect(posts.map((call) => call.path.split("/").pop()).sort()).toEqual(["migrate", "snapshot", "stop"]);
    for (const post of posts) {
      expect(post.body).toEqual({});
      expect(post.headers.get("X-CSRF-Token")).toBeTruthy();
    }
  });

  it("starts a stopped machine without a confirmation and shows the default text", async () => {
    const user = userEvent.setup();
    const { api } = render("db-1", () => true, { [`POST ${base}/db-1/start`]: { status: 204 } });
    await user.click(await screen.findByRole("button", { name: "Start" }));
    await waitFor(() => expect(api.calls.some((call) => call.path.endsWith("/db-1/start"))).toBe(true));
    expect(await screen.findByText("Done")).toBeTruthy();
  });

  it("TestActionsAreHiddenUntilAllowed", async () => {
    const { api } = render("db-1", (check) => check.verb === "update" && check.subresource === "start");
    expect(await screen.findByRole("button", { name: "Start" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Migrate" })).toBeNull();
    expect(screen.queryByRole("tab", { name: "Console" })).toBeNull();
    expect(api.calls.filter((call) => call.path === "/proxy/v1/authz")).toHaveLength(1);
  });

  it("shows migrate and the console tab only when allowed", async () => {
    render("web-1", (check) => check.subresource === "migrate" || check.subresource === "console/vnc");
    expect(await screen.findByRole("button", { name: "Migrate" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "Console" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
  });

  it("TestServerForbiddenAfterShownAction", async () => {
    const user = userEvent.setup();
    render("db-1", () => true, { [`POST ${base}/db-1/start`]: { status: 403, body: { kind: "Status", reason: "Forbidden", message: "forbidden" } } });
    await user.click(await screen.findByRole("button", { name: "Start" }));
    expect(await screen.findByText("You are not allowed to do this")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Start" })).toBeTruthy();
  });

  it("TestStopAndRestartAskForConfirmation and delete asks for the name", async () => {
    const user = userEvent.setup();
    const { api } = render("web-1", () => true, { [`DELETE ${base}/web-1`]: { body: {} } });
    await user.click(await screen.findByRole("button", { name: "Restart" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(api.calls.some((call) => call.method === "POST" && call.path.endsWith("/restart"))).toBe(false);
    void dialog;
    await user.click(screen.getByRole("button", { name: "Delete" }));
    const typed = await screen.findByRole("dialog");
    const confirm = within(typed).getByRole("button", { name: "Delete" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    await user.type(within(typed).getByLabelText("Name"), "web-1");
    await user.click(confirm);
    await waitFor(() => expect(api.calls.some((call) => call.method === "DELETE")).toBe(true));
  });

  it("TestBusyStateDisablesDoubleSubmit", async () => {
    const user = userEvent.setup();
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const { api } = render("db-1", () => true, {
      [`POST ${base}/db-1/start`]: async () => {
        await gate;
        return { body: {} };
      },
    });
    const start = await screen.findByRole("button", { name: "Start" });
    await user.click(start);
    await user.click(start);
    release();
    await waitFor(() => expect(screen.getByText("Done")).toBeTruthy());
    expect(api.calls.filter((call) => call.path.endsWith("/db-1/start"))).toHaveLength(1);
  });
});
