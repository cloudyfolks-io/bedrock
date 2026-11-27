import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { KubeList } from "../../api/types";
import backups from "../../fixtures/virtualmachinebackups.json";
import restores from "../../fixtures/virtualmachinerestores.json";
import { isCron } from "../../resources/validators";
import type { FakeReply, FakeRoute } from "../../test/fakeApi";
import { openApiRoutes } from "../../test/openapi";
import { renderPanel } from "../../test/renderPanel";
import BackupsPage from "./BackupsPage";
import { restoreBody } from "./restoreBody";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const base = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web";
const backupList = backups as unknown as KubeList;
const backup = backupList.items[0] as NonNullable<KubeList["items"][0]>;
const vmPath = `${base}/virtualmachines/web-1`;
const restorePath = `${base}/virtualmachinerestores`;
const existing: FakeReply = { body: { metadata: { name: "web-1", namespace: "acme-web", uid: "vm-uid-1" } } };
const missing: FakeReply = { status: 404, body: { kind: "Status", reason: "NotFound" } };
const archive = {
  ...backup,
  metadata: { ...backup.metadata, name: "web-2-archive", uid: "uid-backup-web-2-archive" },
  status: { phase: "Completed", backups: [{ id: "bk-archive", time: "2026-10-02T10:00:00Z" }] },
};
const pending = {
  ...restores.items[0]!,
  metadata: { ...restores.items[0]!.metadata, name: "pending-restore", uid: "uid-pending-restore" },
  status: { phase: "Pending", conditions: [{ type: "Ready", status: "False", reason: "TargetExists", message: "virtual machine web-1 exists" }] },
};
const failed = {
  ...pending,
  metadata: { ...pending.metadata, name: "failed-restore", uid: "uid-failed-restore" },
  status: { phase: "Failed", conditions: [{ type: "Ready", status: "False", reason: "BackupNotFound", message: "Velero backup bk-001 does not exist" }] },
};

function render(target: FakeRoute = existing, source: FakeRoute = { body: backup }) {
  return renderPanel(
    <Routes>
      <Route path="/p/:project/backups" element={<BackupsPage />} />
    </Routes>,
    {
      route: "/p/web/backups",
      api: {
        ...openApiRoutes(),
        "GET /proxy/v1/projects": { body: projects },
        [`GET ${base}/virtualmachinebackups`]: { body: { ...backupList, items: [...backupList.items, archive] } },
        [`GET ${base}/virtualmachinebackups/${backup.metadata.name}`]: source,
        [`GET ${restorePath}`]: { body: { ...restores, items: [...restores.items, pending, failed] } },
        [`GET ${vmPath}`]: target,
        [`POST ${restorePath}`]: { status: 201, body: {} },
      },
    },
  );
}

async function openRestore(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "Actions for web-1-nightly" }));
  await user.click(await screen.findByRole("menuitem", { name: "Restore" }));
  return within(await screen.findByRole("dialog"));
}

describe("BackupsPage", () => {
  it("lists backups with their history and the restores with phases", async () => {
    render();
    const row = (await screen.findByRole("link", { name: "web-1-nightly" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("web-1")).toBeTruthy();
    expect(within(row).getByText("Completed")).toBeTruthy();
    const history = within(await screen.findByRole("table", { name: "Backups of web-1-nightly" }));
    expect(history.getByText("bk-001")).toBeTruthy();
    expect(history.getByText("20 GiB")).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: "Backup schedules" })).toBeTruthy();
    expect(await screen.findByRole("heading", { level: 2, name: "Virtual machine restores" })).toBeTruthy();
    expect(await screen.findByText("In progress")).toBeTruthy();
    const pendingRow = within(screen.getByText("pending-restore").closest("tr")!);
    expect(pendingRow.getByText("Pending")).toBeTruthy();
    expect(pendingRow.getByText("TargetExists")).toBeTruthy();
    expect(pendingRow.getByText("virtual machine web-1 exists")).toBeTruthy();
    const failedRow = within(screen.getByText("failed-restore").closest("tr")!);
    expect(failedRow.getByText("Failed")).toBeTruthy();
    expect(failedRow.getByText("BackupNotFound")).toBeTruthy();
    expect(failedRow.getByText("Velero backup bk-001 does not exist")).toBeTruthy();
  });

  it("only offers history for matching schedules and hides it when none match", async () => {
    const user = userEvent.setup();
    render();
    await user.click(await screen.findByRole("combobox", { name: "Backup schedule" }));
    await user.click(await screen.findByRole("option", { name: "web-2-archive" }));
    expect(screen.getByRole("table", { name: "Backups of web-2-archive" })).toBeTruthy();
    expect(screen.queryByText("bk-001")).toBeNull();

    const [filter] = screen.getAllByRole("textbox", { name: "Filter by name" });
    await user.type(filter!, "nightly");
    expect(screen.getByRole("table", { name: "Backups of web-1-nightly" })).toBeTruthy();
    expect(screen.queryByText("bk-archive")).toBeNull();
    await user.type(filter!, "not-found");
    expect(screen.getByText("No match")).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Backup history" })).toBeNull();
    expect(screen.queryByRole("table", { name: /^Backups of/ })).toBeNull();
  });

  it("TestCronIsValidated", () => {
    expect(isCron("0 2 * * *")).toBe(true);
    expect(isCron("*/5 * * * *")).toBe(true);
    expect(isCron("0 2 * *")).toBe(false);
    expect(isCron("every day")).toBe(false);
  });

  it("refuses an invalid cron in the create form", async () => {
    const user = userEvent.setup();
    const { api } = render();
    await user.click(await screen.findByRole("button", { name: "Create" }));
    await user.type(await screen.findByLabelText("Name", undefined, { timeout: 15000 }), "nightly");
    await user.type(screen.getByLabelText(/^vmRef/), "web-1");
    await user.type(screen.getByLabelText(/^target/), "s3://b");
    await user.type(screen.getByLabelText(/^schedule/), "every day");
    await user.click(document.querySelector("form button[type=submit]") as HTMLElement);
    expect(await screen.findByText(/five fields/)).toBeTruthy();
    expect(api.calls.some((call) => call.method === "POST" && call.path !== "/proxy/v1/authz")).toBe(false);
  });
});

describe("restoreBody", () => {
  it.each([
    [true, { replace: true }],
    [false, { targetName: "web-1" }],
  ])("sends exactly one CRD restore mode (replace=%s)", (replace, mode) => {
    expect(restoreBody(backup, { backupID: "bk-001", replace, suffix: "x1" })).toEqual({
      apiVersion: "bedrock.cloudyfolks.io/v1alpha1",
      kind: "VirtualMachineRestore",
      metadata: { name: "web-1-nightly-x1", namespace: "acme-web" },
      spec: { backupRef: "web-1-nightly", backupID: "bk-001", ...mode },
    });
  });
});

describe("RestoreDialog", () => {
  it("blocks an existing target without explicit replacement and exact confirmation", async () => {
    const user = userEvent.setup();
    const { api } = render();
    const dialog = await openRestore(user);
    const confirm = dialog.getByRole("button", { name: "Restore" }) as HTMLButtonElement;
    expect(await dialog.findByText(/^The target already exists/)).toBeTruthy();
    expect(confirm.disabled).toBe(true);
    expect(dialog.queryByLabelText(/to confirm/)).toBeNull();
    expect(dialog.getByRole("switch").getAttribute("aria-checked")).toBe("false");
    await user.click(dialog.getByRole("switch"));
    expect(confirm.disabled).toBe(true);
    await user.type(dialog.getByLabelText("Type web-1 to confirm"), "web-1");
    expect(confirm.disabled).toBe(false);
    await user.click(confirm);
    await waitFor(() => expect(api.calls.some((call) => call.method === "POST" && call.path === restorePath)).toBe(true));
    const body = api.calls.find((call) => call.method === "POST" && call.path === restorePath)?.body as { metadata: { name: string } };
    expect(body).toEqual(restoreBody(backup, { backupID: "bk-001", replace: true, suffix: body.metadata.name.replace("web-1-nightly-", "") }));
    expect(api.calls.filter((call) => call.method === "GET" && call.path === vmPath)).toHaveLength(2);
  });

  it("requires an existing VM for replacement but can restore a missing target", async () => {
    const user = userEvent.setup();
    const { api } = render(missing);
    const dialog = await openRestore(user);
    const confirm = dialog.getByRole("button", { name: "Restore" }) as HTMLButtonElement;
    await dialog.findByText(/^The target does not exist/);
    expect(confirm.disabled).toBe(false);
    await user.click(dialog.getByRole("switch"));
    expect(dialog.getByText(/^Replacement requires an existing target/)).toBeTruthy();
    expect((dialog.getByLabelText("Type web-1 to confirm") as HTMLInputElement).disabled).toBe(true);
    expect(confirm.disabled).toBe(true);
    await user.click(dialog.getByRole("switch"));
    await user.click(confirm);
    await waitFor(() => expect(api.calls.find((call) => call.method === "POST" && call.path === restorePath)?.body).toMatchObject({ spec: { targetName: "web-1" } }));
  });

  it.each([
    [{ status: 403, body: { kind: "Status", reason: "Forbidden" } }, "Could not check the target virtual machine."],
    [{ status: 404, body: { error: "not_found" } }, "Could not check the target virtual machine."],
    [{ body: { metadata: { name: "web-1", namespace: "acme-web" } } }, "The target virtual machine could not be identified."],
  ] as const)("does not treat an unknown or failed lookup as a missing VM", async (response, message) => {
    const user = userEvent.setup();
    const { api } = render(response);
    const dialog = await openRestore(user);
    expect(await dialog.findByText(message)).toBeTruthy();
    await user.click(dialog.getByRole("button", { name: "Restore" }));
    expect((dialog.getByRole("button", { name: "Restore" }) as HTMLButtonElement).disabled).toBe(true);
    expect(api.calls.some((call) => call.method === "POST" && call.path === restorePath)).toBe(false);
  });

  it("rechecks the VM before submitting if it appears after the dialog opens", async () => {
    const user = userEvent.setup();
    let response = missing;
    const { api } = render(() => response);
    const dialog = await openRestore(user);
    await dialog.findByText(/^The target does not exist/);
    response = existing;
    const confirm = dialog.getByRole("button", { name: "Restore" }) as HTMLButtonElement;
    await user.click(confirm);
    expect(await dialog.findByText(/^The target already exists/)).toBeTruthy();
    expect(confirm.disabled).toBe(true);
    expect(api.calls.some((call) => call.method === "POST" && call.path === restorePath)).toBe(false);
  });

  it("clears replacement confirmation if a different VM has the same name", async () => {
    const user = userEvent.setup();
    let response = existing;
    const { api } = render(() => response);
    const dialog = await openRestore(user);
    await dialog.findByText(/^The target already exists/);
    await user.click(dialog.getByRole("switch"));
    const name = dialog.getByLabelText("Type web-1 to confirm") as HTMLInputElement;
    await user.type(name, "web-1");
    const confirm = dialog.getByRole("button", { name: "Restore" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(false);

    response = { body: { metadata: { name: "web-1", namespace: "acme-web", uid: "vm-uid-2" } } };
    await user.click(confirm);
    expect(await dialog.findByText(/^The target changed/)).toBeTruthy();
    expect(name.value).toBe("");
    expect(confirm.disabled).toBe(true);
    expect(api.calls.some((call) => call.method === "POST" && call.path === restorePath)).toBe(false);

    await user.type(name, "web-1");
    await user.click(confirm);
    await waitFor(() => expect(api.calls.filter((call) => call.method === "POST" && call.path === restorePath)).toHaveLength(1));
  });

  it("cannot replace a VM when the backup now points to a different source", async () => {
    const user = userEvent.setup();
    const { api } = render(existing, { body: { ...backup, spec: { ...backup.spec, vmRef: "web-2" } } });
    const dialog = await openRestore(user);
    await dialog.findByText(/^The target already exists/);
    await user.click(dialog.getByRole("switch"));
    await user.type(dialog.getByLabelText("Type web-1 to confirm"), "web-1");
    const confirm = dialog.getByRole("button", { name: "Restore" }) as HTMLButtonElement;
    await user.click(confirm);
    expect(await dialog.findByText(/^The backup changed/)).toBeTruthy();
    expect(confirm.disabled).toBe(true);
    expect(api.calls.some((call) => call.method === "POST" && call.path === restorePath)).toBe(false);
  });
});
