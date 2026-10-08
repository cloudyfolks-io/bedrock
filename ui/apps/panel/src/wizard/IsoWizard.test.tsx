import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import { FakeXhr } from "../test/fakeXhr";
import { choose, ns, renderWizard } from "./testSupport";
import IsoWizard from "./IsoWizard";

const path = "/p/:project/vms/new-iso";
const isoImage = (name: string, phase = "Ready") => ({
  apiVersion: "bedrock.cloudyfolks.io/v1alpha1",
  kind: "Image",
  metadata: { name, namespace: "acme-web", uid: `uid-${name}`, resourceVersion: "1" },
  spec: { type: "iso", source: { upload: {} } },
  status: { phase },
});
const imageList = (...items: unknown[]) => ({ items, metadata: { resourceVersion: "1" } });

async function next(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: /^Next/ }));
}

describe("IsoWizard", () => {
  beforeEach(() => FakeXhr.install());

  it("lists only ready ISO images and offers an upload", async () => {
    const user = userEvent.setup();
    renderWizard(<IsoWizard />, path, { api: { [`GET ${ns}/images`]: { body: imageList(isoImage("ubuntu-iso"), isoImage("pending-iso", "Importing")) } } });
    await user.type(await screen.findByLabelText("Name"), "win-1");
    await next(user);
    await user.click(await screen.findByRole("combobox", { name: /Installation ISO/ }));
    expect((await screen.findAllByRole("option")).map((option) => option.textContent)).toEqual(["ubuntu-iso (project)"]);
    await user.keyboard("{Escape}");
    expect(screen.getByRole("button", { name: "Upload a new ISO" })).toBeTruthy();
  });

  it("Windows preferences add the virtio-win cdrom and default the firmware, then submit and open the console", async () => {
    const user = userEvent.setup();
    const { api } = renderWizard(<IsoWizard />, path, {
      api: { [`GET ${ns}/images`]: { body: imageList(isoImage("win-iso")) }, [`POST ${ns}/virtualmachines`]: { status: 201, body: {} } },
    });
    await user.type(await screen.findByLabelText("Name"), "win-1");
    await next(user);
    await choose(user, /Installation ISO/, "win-iso (project)");
    await user.click(screen.getByRole("button", { name: "windows.2k22" }));
    await user.click(await screen.findByRole("checkbox", { name: "Attach the virtio-win drivers" }));
    await next(user);
    await choose(user, /Instance type/, /u1\.large/);
    await next(user);
    expect(await screen.findByRole("switch", { name: "UEFI" })).toBeTruthy();
    expect(screen.getByRole("switch", { name: "TPM" }).getAttribute("aria-checked")).toBe("true");
    await next(user);
    await next(user);
    await next(user);
    await user.click(screen.getByRole("button", { name: "Create virtual machine" }));
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/p/web/vms/win-1?tab=console"));
    const post = api.calls.find((call) => call.method === "POST" && call.path.endsWith("/virtualmachines"));
    expect(post?.body).toMatchObject({
      spec: { preference: "windows.2k22", cdroms: [{ imageRef: "win-iso" }, { imageRef: "virtio-win" }], firmware: { uefi: true, secureBoot: false, tpm: true } },
    });
  });

  it("uploads a new ISO and selects it", async () => {
    const user = userEvent.setup();
    const grant = { url: "https://upload.lab.example.test/v1beta1/upload-async", token: "tok", expiresAt: "2026-10-09T00:00:00Z" };
    renderWizard(<IsoWizard />, path, {
      api: {
        [`GET ${ns}/images`]: { body: imageList() },
        [`POST ${ns}/images`]: { status: 201, body: {} },
        [`GET ${ns}/images/fresh-iso`]: { body: isoImage("fresh-iso", "WaitingForUpload") },
        [`POST ${ns}/images/fresh-iso/upload`]: { body: grant },
      },
    });
    await user.type(await screen.findByLabelText("Name"), "win-1");
    await next(user);
    await user.click(await screen.findByRole("button", { name: "Upload a new ISO" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("Name"), "fresh-iso");
    await user.click(within(dialog).getByRole("button", { name: "Create" }));
    const upload = await screen.findByRole("dialog", { name: "Upload fresh-iso" }, { timeout: 5000 });
    await user.upload(upload.querySelector('input[type="file"]') as HTMLInputElement, new File(["x"], "w.iso"));
    await user.click(within(upload).getByRole("button", { name: "Upload" }));
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1));
    FakeXhr.latest().finish(200);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByRole("combobox", { name: /Installation ISO/ }).textContent).toContain("fresh-iso");
  });
});
