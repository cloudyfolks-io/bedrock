import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { KubeList } from "../../api/types";
import backups from "../../fixtures/virtualmachinebackups.json";
import restores from "../../fixtures/virtualmachinerestores.json";
import { isCron } from "../../resources/validators";
import { openApiRoutes } from "../../test/openapi";
import { renderPanel } from "../../test/renderPanel";
import BackupsPage from "./BackupsPage";
import { restoreBody } from "./restoreBody";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const base = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web";
const backupList = backups as unknown as KubeList;
const backup = backupList.items[0] as NonNullable<KubeList["items"][0]>;

function render() {
  return renderPanel(
    <Routes>
      <Route path="/p/:project/backups" element={<BackupsPage />} />
    </Routes>,
    {
      route: "/p/web/backups",
      api: {
        ...openApiRoutes(),
        "GET /proxy/v1/projects": { body: projects },
        [`GET ${base}/virtualmachinebackups`]: { body: backupList },
        [`GET ${base}/virtualmachinerestores`]: { body: restores as unknown as KubeList },
        [`POST ${base}/virtualmachinerestores`]: { status: 201, body: {} },
      },
    },
  );
}

describe("BackupsPage", () => {
  it("lists backups with their history and the restores with phases", async () => {
    render();
    const row = (await screen.findByRole("link", { name: "web-1-nightly" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("web-1")).toBeTruthy();
    expect(within(row).getByText("Completed")).toBeTruthy();
    expect(await screen.findByText("bk-001")).toBeTruthy();
    expect(screen.getByText("20 GiB")).toBeTruthy();
    expect(await screen.findByText("In progress")).toBeTruthy();
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
  it("fixes the target name to the backed up machine", () => {
    expect(restoreBody(backup, { backupID: "bk-001", replace: true, suffix: "x1" })).toEqual({
      apiVersion: "bedrock.cloudyfolks.io/v1alpha1",
      kind: "VirtualMachineRestore",
      metadata: { name: "web-1-nightly-x1", namespace: "acme-web" },
      spec: { backupRef: "web-1-nightly", backupID: "bk-001", targetName: "web-1", replace: true },
    });
  });
});

describe("RestoreDialog", () => {
  it("TestReplaceNeedsTypedConfirmation and posts exactly the restoreBody", async () => {
    const user = userEvent.setup();
    const { api } = render();
    await user.click(await screen.findByRole("button", { name: "Actions for web-1-nightly" }));
    await user.click(await screen.findByRole("menuitem", { name: "Restore" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).queryByLabelText(/Name/)).toBeNull();
    await user.click(within(dialog).getByRole("switch"));
    const confirm = within(dialog).getByRole("button", { name: "Restore" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    await user.type(within(dialog).getByLabelText("Type web-1 to confirm"), "web-1");
    expect(confirm.disabled).toBe(false);
    await user.click(confirm);
    await waitFor(() => expect(api.calls.some((call) => call.method === "POST" && call.path !== "/proxy/v1/authz")).toBe(true));
    const body = api.calls.find((call) => call.method === "POST" && call.path !== "/proxy/v1/authz")?.body as { metadata: { name: string } };
    expect(body).toEqual(restoreBody(backup, { backupID: "bk-001", replace: true, suffix: body.metadata.name.replace("web-1-nightly-", "") }));
  });
});
