import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import virtualmachines from "../fixtures/virtualmachines.json";
import volumes from "../fixtures/volumes.json";
import { axeViolations } from "../test/axe";
import type { FakeReply } from "../test/fakeApi";
import { buildDiskVirtualMachine } from "./buildVirtualMachine";
import { instanceTypes } from "./instanceTypes";
import { choose, ns, renderWizard } from "./testSupport";
import VmWizard from "./VmWizard";

const path = "/p/:project/vms/new";

async function next(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: /^Next/ }));
}

async function toDisks(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByLabelText("Name"), "new-vm");
  await next(user);
  await choose(user, /^Image/, "ubuntu-24-04 (cluster)");
  await next(user);
  await choose(user, /Instance type/, /u1\.medium/);
  await next(user);
}

async function toReview(user: ReturnType<typeof userEvent.setup>) {
  await toDisks(user);
  await next(user);
  await next(user);
  await next(user);
}

describe("VmWizard", () => {
  it("TestEachStepValidatesBeforeNext", async () => {
    const user = userEvent.setup();
    renderWizard(<VmWizard />, path);
    const name = await screen.findByLabelText("Name");
    await user.type(name, "Bad_Name");
    await next(user);
    expect(await screen.findByText(/lowercase letters/)).toBeTruthy();
    expect(document.activeElement).toBe(name);
    await user.clear(name);
    await user.type(name, "web-1");
    await next(user);
    expect(await screen.findByText("A virtual machine with this name already exists")).toBeTruthy();
    await user.clear(name);
    await user.type(name, "new-vm");
    await next(user);
    expect(await screen.findByRole("heading", { level: 2, name: "Image" })).toBeTruthy();
    await next(user);
    expect(await screen.findByText("Choose an image")).toBeTruthy();
    await choose(user, /^Image/, "ubuntu-24-04 (cluster)");
    await next(user);
    await next(user);
    expect(await screen.findByText("Choose an instance type")).toBeTruthy();
    await choose(user, /Instance type/, /u1\.small/);
    await next(user);
    const root = await screen.findByRole("textbox", { name: /Root disk size/ });
    await user.clear(root);
    await user.type(root, "5");
    await user.tab();
    await next(user);
    expect(await screen.findByText("The root disk is smaller than the image")).toBeTruthy();
    await user.clear(root);
    await user.type(root, "20");
    await user.tab();
    await next(user);
    expect(await screen.findByRole("heading", { level: 2, name: "Network" })).toBeTruthy();
    await next(user);
    const userData = await screen.findByLabelText("Cloud-init user data");
    await user.click(userData);
    await user.paste("hello");
    await next(user);
    expect(await screen.findByText(/must start with #cloud-config/)).toBeTruthy();
  });

  it("TestImageStepListsDiskImagesOfBothScopes", async () => {
    const user = userEvent.setup();
    renderWizard(<VmWizard />, path);
    await user.type(await screen.findByLabelText("Name"), "new-vm");
    await next(user);
    await user.click(await screen.findByRole("combobox", { name: /^Image/ }));
    const options = (await screen.findAllByRole("option")).map((option) => option.textContent);
    expect(options).toEqual(["custom-disk (project)", "ubuntu-24-04 (cluster)"]);
  });

  it("TestSizeStepShowsVcpuAndMemory", async () => {
    const user = userEvent.setup();
    renderWizard(<VmWizard />, path);
    await user.type(await screen.findByLabelText("Name"), "new-vm");
    await next(user);
    await choose(user, /^Image/, "ubuntu-24-04 (cluster)");
    await next(user);
    await user.click(await screen.findByRole("combobox", { name: /Instance type/ }));
    const options = (await screen.findAllByRole("option")).map((option) => option.textContent);
    expect(options).toEqual(instanceTypes.map((type) => `${type.name} · ${type.vcpu} vCPU · ${type.memoryGiB} GiB`));
  });

  it("TestCustomSizeAsksForCoresAndMemory", async () => {
    const user = userEvent.setup();
    renderWizard(<VmWizard />, path);
    await user.type(await screen.findByLabelText("Name"), "new-vm");
    await next(user);
    await choose(user, /^Image/, "ubuntu-24-04 (cluster)");
    await next(user);
    await user.click(await screen.findByRole("switch", { name: "Custom size" }));
    expect(screen.queryByRole("combobox", { name: /Instance type/ })).toBeNull();
    await next(user);
    expect(await screen.findByText("Enter the number of CPU cores")).toBeTruthy();
    expect(await screen.findByText("Enter at least 128 MiB of memory")).toBeTruthy();
    await user.type(screen.getByRole("textbox", { name: /CPU cores/ }), "6");
    await user.type(screen.getByRole("textbox", { name: /Memory \(MiB\)/ }), "6144");
    await user.tab();
    await next(user);
    expect(await screen.findByRole("heading", { level: 2, name: "Disks" })).toBeTruthy();
  });

  it("TestReviewShowsTheObjectAndSubmits", async () => {
    const user = userEvent.setup();
    let resolve!: (reply: FakeReply) => void;
    const pending = new Promise<FakeReply>((reply) => { resolve = reply; });
    const { api } = renderWizard(<VmWizard />, path, { api: { [`POST ${ns}/virtualmachines`]: () => pending } });
    await toReview(user);
    expect(screen.getByText("ubuntu-24-04")).toBeTruthy();
    expect(screen.getByText("20 GiB")).toBeTruthy();
    expect(screen.getByText("default")).toBeTruthy();
    const details = document.querySelector("details");
    expect(details?.open).toBe(false);
    await user.tab();
    const summary = screen.getByText("Advanced specification (JSON)");
    expect(document.activeElement).toBe(summary);
    await user.click(summary);
    expect(details?.open).toBe(true);
    const pre = document.querySelector("pre");
    expect(pre?.getAttribute("dir")).toBe("ltr");
    expect(pre?.textContent).toContain('"instanceType": "u1.medium"');
    await user.tab();
    expect(document.activeElement).toBe(pre);
    const create = screen.getByRole("button", { name: "Create virtual machine" });
    await user.click(create);
    expect(create.matches(":disabled")).toBe(true);
    expect(screen.queryByText("Request accepted")).toBeNull();
    resolve({ status: 201, body: {} });
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/p/web/vms/new-vm"));
    expect(screen.getByText("Request accepted")).toBeTruthy();
    expect(screen.queryByRole("alertdialog")).toBeNull();
    const post = api.calls.find((call) => call.method === "POST" && call.path.endsWith("/virtualmachines"));
    expect(post?.url).toContain("fieldManager=bedrock-panel");
    expect(post?.body).toEqual(
      buildDiskVirtualMachine(
        { name: "new-vm", imageName: "ubuntu-24-04", imageScope: "cluster", instanceType: "u1.medium", customSize: false, cores: null, memoryMiB: null, preference: "ubuntu", rootDiskGi: 20, storageClass: "", extraVolumes: [], subnetRef: "default", securityGroups: [], sshKeys: [], userData: "", running: true },
        { namespace: "acme-web" },
      ),
    );
  });

  it("only lets the user select available project volumes and names the attachment owner", async () => {
    const user = userEvent.setup();
    const foreign = { metadata: { namespace: "another-project", name: "other-volume" }, status: { phase: "Available" } };
    const reserved = { metadata: { namespace: "acme-web", name: "reserved" }, status: { phase: "Available", conditions: [{ type: "Ready", status: "True" }] } };
    const claims = [
      { metadata: { namespace: "acme-web", name: "waiting-vm" }, spec: { rootDisk: { volumeRef: "reserved" } } },
      { metadata: { namespace: "another-project", name: "foreign-vm" }, spec: { disks: [{ volumeRef: "scratch" }] } },
    ];
    const { api } = renderWizard(<VmWizard />, path, { api: {
      [`GET ${ns}/volumes`]: { body: { ...volumes, items: [...volumes.items, foreign, reserved] } },
      [`GET ${ns}/virtualmachines`]: { body: { ...virtualmachines, items: [...virtualmachines.items, ...claims] } },
      [`POST ${ns}/virtualmachines`]: { status: 201, body: {} },
    } });
    await toDisks(user);
    const used = await screen.findByRole("checkbox", { name: "data-1 · Attached to web-1" });
    expect(used.matches(":disabled, [aria-disabled='true']")).toBe(true);
    expect(screen.getByRole("checkbox", { name: "reserved · Referenced by VM waiting-vm" }).getAttribute("aria-disabled")).toBe("true");
    expect(screen.queryByRole("checkbox", { name: "other-volume" })).toBeNull();
    await user.click(used);
    expect(used.getAttribute("aria-checked")).toBe("false");
    await user.click(screen.getByRole("checkbox", { name: "scratch" }));
    await next(user);
    await next(user);
    await next(user);
    expect(screen.getByText("scratch")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Create virtual machine" }));
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/p/web/vms/new-vm"));
    expect(api.calls.find((call) => call.method === "POST" && call.path === `${ns}/virtualmachines`)?.body).toMatchObject({
      spec: { disks: [{ volumeRef: "scratch", bus: "virtio" }] },
    });
  });

  it("does not report attachment owners when the project volume list has not loaded", async () => {
    const user = userEvent.setup();
    let resolve!: (reply: FakeReply) => void;
    const pending = new Promise<FakeReply>((reply) => { resolve = reply; });
    renderWizard(<VmWizard />, path, { api: { [`GET ${ns}/volumes`]: () => pending } });
    await toDisks(user);
    expect(screen.getByText("Loading project volumes…")).toBeTruthy();
    resolve({ status: 403, body: { error: "forbidden" } });
    expect(await screen.findByText("Could not load project volumes.")).toBeTruthy();
    expect(screen.queryByText(/Attached to/)).toBeNull();
    await next(user);
    expect(await screen.findByRole("heading", { name: "Network", level: 2 })).toBeTruthy();
  });

  it.each([
    ["attached", "Attached to Container/stateful"],
    ["root claim", "Referenced by VM new-owner"],
    ["data claim", "Referenced by VM new-owner"],
    ["not ready", "Not available to attach"],
    ["volume list failure", ""],
    ["VM list failure", ""],
  ])("rechecks a selected volume at Create: %s", async (outcome, reason) => {
    const user = userEvent.setup();
    let changed = false;
    const currentVolumes = { ...volumes, items: volumes.items.map((volume) => {
      if (volume.metadata.name !== "scratch") return volume;
      if (outcome === "attached") return { ...volume, status: { phase: "InUse", attachedTo: "Container/stateful" } };
      if (outcome === "not ready") return { ...volume, status: { phase: "Available", conditions: [{ type: "Ready", status: "False" }] } };
      return volume;
    }) };
    const currentVms = outcome === "root claim" || outcome === "data claim"
      ? { ...virtualmachines, items: [...virtualmachines.items, {
        metadata: { namespace: "acme-web", name: "new-owner" },
        spec: outcome === "root claim" ? { rootDisk: { volumeRef: "scratch" } } : { disks: [{ volumeRef: "scratch" }] },
      }] }
      : virtualmachines;
    const unavailable = { status: 503, body: { error: "unavailable" } };
    const { api } = renderWizard(<VmWizard />, path, { api: {
      [`GET ${ns}/volumes`]: () => !changed ? { body: volumes } : outcome === "volume list failure" ? unavailable : { body: currentVolumes },
      [`GET ${ns}/virtualmachines`]: () => !changed ? { body: virtualmachines } : outcome === "VM list failure" ? unavailable : { body: currentVms },
    } });
    await toDisks(user);
    await user.click(await screen.findByRole("checkbox", { name: "scratch" }));
    await next(user);
    await next(user);
    await next(user);
    changed = true;
    await user.click(screen.getByRole("button", { name: "Create virtual machine" }));
    const error = await screen.findByRole("alert");
    if (reason) {
      expect(error.textContent).toBe("Remove scratch to continue.");
      expect(screen.getByRole("checkbox", { name: (name) => name.endsWith(`scratch · ${reason}`), checked: true }).getAttribute("aria-disabled")).toBe("true");
    } else {
      expect(error.textContent).toContain("Could not verify the selected volumes.");
      expect(screen.queryByText(/Attached to/)).toBeNull();
    }
    expect(document.activeElement).toBe(error);
    expect(api.calls.some((call) => call.method === "POST" && call.path === `${ns}/virtualmachines`)).toBe(false);
    await user.click(screen.getByRole("button", { name: "Remove scratch" }));
    await next(user);
    expect(await screen.findByRole("heading", { name: "Network", level: 2 })).toBeTruthy();
  });

  it("TestSubmitErrorsMapToSteps", async () => {
    const user = userEvent.setup();
    renderWizard(<VmWizard />, path, {
      api: { [`POST ${ns}/virtualmachines`]: { status: 422, body: { kind: "Status", reason: "Invalid", message: "bad", details: { causes: [{ field: "spec.rootDisk.sizeGi", message: "too small for the image" }] } } } },
    });
    await toReview(user);
    await user.click(screen.getByRole("button", { name: "Create virtual machine" }));
    expect(await screen.findByRole("heading", { level: 2, name: "Disks" })).toBeTruthy();
    expect(screen.getByText("too small for the image")).toBeTruthy();
  });

  it("returns to Basics when the name exists", async () => {
    const user = userEvent.setup();
    renderWizard(<VmWizard />, path, { api: { [`POST ${ns}/virtualmachines`]: { status: 409, body: { kind: "Status", reason: "AlreadyExists", message: "exists" } } } });
    await toReview(user);
    await user.click(screen.getByRole("button", { name: "Create virtual machine" }));
    expect(await screen.findByRole("heading", { level: 2, name: "Basics" })).toBeTruthy();
    expect(screen.getByText("An object with this name already exists")).toBeTruthy();
  });

  it("TestBackKeepsValues and Enter never submits early", async () => {
    const user = userEvent.setup();
    const { api } = renderWizard(<VmWizard />, path);
    const name = await screen.findByLabelText("Name");
    await user.type(name, "new-vm{Enter}");
    expect(await screen.findByRole("heading", { level: 2, name: "Image" })).toBeTruthy();
    expect(api.calls.some((call) => call.method === "POST" && !call.path.includes("authz"))).toBe(false);
    await user.click(screen.getByRole("button", { name: /Back/ }));
    expect((await screen.findByLabelText("Name") as HTMLInputElement).value).toBe("new-vm");
  });

  it("leaves without warning when only the subnet default changes", async () => {
    const user = userEvent.setup();
    const { queryClient } = renderWizard(<VmWizard />, path, {
      api: { [`GET ${ns}/subnets`]: { body: { items: [{ metadata: { name: "default" } }, { metadata: { name: "other" } }] } } },
    });
    await screen.findByLabelText("Name");
    await act(() => {
      queryClient.setQueryData(["wizard", "project", "web"], { metadata: { name: "web" }, status: { subnet: "other" } });
    });
    await user.click(await screen.findByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/p/web/vms"));
  });

  it("asks before discarding typed values", async () => {
    const user = userEvent.setup();
    renderWizard(<VmWizard />, path);
    await user.type(await screen.findByLabelText("Name"), "typed");
    const unload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(unload);
    expect(unload.defaultPrevented).toBe(true);
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(screen.getByTestId("path").textContent).toBe("/p/web/vms/new");
    await user.click(within(dialog).getByRole("button", { name: "Discard" }));
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/p/web/vms"));
  });

  it("TestWizardIsAccessible", async () => {
    const user = userEvent.setup();
    const { container } = renderWizard(<VmWizard />, path);
    await screen.findByLabelText("Name");
    const steps = screen.getByRole("list", { name: "Steps" });
    expect(within(steps).getAllByRole("listitem")[0]?.getAttribute("aria-current")).toBe("step");
    expect(screen.getByText("Step 1 of 7")).toBeTruthy();
    await user.type(screen.getByLabelText("Name"), "new-vm");
    await next(user);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("heading", { level: 2, name: "Image" })));
    expect(within(steps).getAllByRole("listitem")[1]?.getAttribute("aria-current")).toBe("step");
    expect(screen.getByText("Step 2 of 7")).toBeTruthy();
    expect(await axeViolations(container)).toEqual([]);
  });

  it("TestWizardRtl mirrors the arrows", async () => {
    renderWizard(<VmWizard />, path, { locale: "fa" });
    await screen.findByLabelText("نام");
    expect(document.documentElement.dir).toBe("rtl");
    expect(document.querySelectorAll(".br-icon--directional").length).toBeGreaterThan(1);
  });

  it("TestWizardNeedsCreatePermission", async () => {
    renderWizard(<VmWizard />, path, { allow: (check) => check.verb !== "create" });
    expect(await screen.findByText("You cannot create virtual machines here")).toBeTruthy();
  });
});
