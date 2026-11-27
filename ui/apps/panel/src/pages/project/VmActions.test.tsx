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

async function openActions(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "More VM actions" }));
  return within(await screen.findByRole("menu"));
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
  it("promotes Stop and posts permitted overflow operations to their subresources", async () => {
    const user = userEvent.setup();
    const { api } = render("web-1", () => true, { [`POST ${base}/web-1/*`]: { body: { message: "snapshot started" } } });
    expect((await screen.findByRole("button", { name: "Stop" })).classList.contains("br-button--primary")).toBe(true);
    expect(screen.queryByRole("button", { name: "Snapshot" })).toBeNull();
    const menu = await openActions(user);
    expect(menu.getByRole("menuitem", { name: "Start" }).getAttribute("aria-disabled")).toBe("true");
    expect(menu.queryByRole("menuitem", { name: "Stop" })).toBeNull();
    await user.click(menu.getByRole("menuitem", { name: "Snapshot" }));
    expect(await screen.findByText("snapshot started")).toBeTruthy();
    await user.click((await openActions(user)).getByRole("menuitem", { name: "Migrate" }));
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
    expect(await screen.findByText("Request accepted")).toBeTruthy();
  });

  it("TestActionsAreHiddenUntilAllowed", async () => {
    const { api } = render("db-1", (check) => check.verb === "update" && check.subresource === "start");
    expect(await screen.findByRole("button", { name: "Start" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "More VM actions" })).toBeNull();
    expect(screen.queryByRole("tab", { name: "Console" })).toBeNull();
    expect(api.calls.filter((call) => call.path === "/proxy/v1/authz")).toHaveLength(1);
  });

  it("shows migrate and the console tab only when allowed", async () => {
    const user = userEvent.setup();
    render("web-1", (check) => check.subresource === "migrate" || check.subresource === "console/vnc");
    const menu = await openActions(user);
    expect(menu.getByRole("menuitem", { name: "Migrate" })).toBeTruthy();
    expect(menu.queryByRole("menuitem", { name: "Delete" })).toBeNull();
    await user.keyboard("{Escape}");
    expect(screen.getByRole("tab", { name: "Console" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
  });

  it("keeps transitional actions disabled and does not promote Delete", async () => {
    const user = userEvent.setup();
    const vmBefore = vm("web-1");
    render("web-1", () => true, { [`GET ${base}/web-1`]: { body: { ...vmBefore, status: { ...vmBefore.status, phase: "Migrating" } } } });
    const menu = await openActions(user);
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
    const migrate = menu.getByRole("menuitem", { name: "Migrate" });
    expect(migrate.getAttribute("aria-disabled")).toBe("true");
    expect(menu.getByRole("menuitem", { name: "Delete" }).getAttribute("aria-disabled")).toBe("true");
  });

  it("TestServerForbiddenAfterShownAction", async () => {
    const user = userEvent.setup();
    render("db-1", () => true, { [`POST ${base}/db-1/start`]: { status: 403, body: { kind: "Status", reason: "Forbidden", message: "forbidden" } } });
    await user.click(await screen.findByRole("button", { name: "Start" }));
    expect(await screen.findByText("You are not allowed to do this")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Start" })).toBeTruthy();
  });

  it("confirms Restart and separates Delete behind typed confirmation", async () => {
    const user = userEvent.setup();
    const { api } = render("web-1", () => true, { [`DELETE ${base}/web-1`]: { body: {} } });
    await user.click((await openActions(user)).getByRole("menuitem", { name: "Restart" }));
    expect(await screen.findByRole("alertdialog")).toBeTruthy();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(api.calls.some((call) => call.method === "POST" && call.path.endsWith("/restart"))).toBe(false);
    const menu = await openActions(user);
    expect(menu.getByRole("separator")).toBeTruthy();
    const remove = menu.getByRole("menuitem", { name: "Delete" });
    expect(remove.getAttribute("data-tone")).toBe("danger");
    await user.click(remove);
    const typed = await screen.findByRole("dialog");
    const confirm = within(typed).getByRole("button", { name: "Delete" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    await user.type(within(typed).getByLabelText("Name"), "web-1");
    await user.click(confirm);
    await waitFor(() => expect(api.calls.some((call) => call.method === "DELETE")).toBe(true));
  });

  it("shows a pending action without replacing live health or allowing another submit", async () => {
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
    const start = await screen.findByRole<HTMLButtonElement>("button", { name: "Start" });
    await user.click(start);
    const health = within(start.closest("header") as HTMLElement);
    expect(health.getByText("Stopped")).toBeTruthy();
    expect(await health.findByText("Requesting action")).toBeTruthy();
    expect(start.disabled).toBe(true);
    expect(health.getByRole<HTMLButtonElement>("button", { name: "More VM actions" }).disabled).toBe(true);
    await user.click(start);
    release();
    await waitFor(() => expect(screen.getByText("Request accepted")).toBeTruthy());
    expect(api.calls.filter((call) => call.path.endsWith("/db-1/start"))).toHaveLength(1);
  });
});
