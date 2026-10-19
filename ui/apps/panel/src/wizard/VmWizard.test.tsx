import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { axeViolations } from "../test/axe";
import { buildDiskVirtualMachine } from "./buildVirtualMachine";
import { instanceTypes } from "./instanceTypes";
import { choose, ns, renderWizard } from "./testSupport";
import VmWizard from "./VmWizard";

const path = "/p/:project/vms/new";

async function next(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: /^Next/ }));
}

async function toReview(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByLabelText("Name"), "new-vm");
  await next(user);
  await choose(user, /^Image/, "ubuntu-24-04 (cluster)");
  await next(user);
  await choose(user, /Instance type/, /u1\.medium/);
  await next(user);
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
    const { api } = renderWizard(<VmWizard />, path, { api: { [`POST ${ns}/virtualmachines`]: { status: 201, body: {} } } });
    await toReview(user);
    const pre = document.querySelector("pre");
    expect(pre?.getAttribute("dir")).toBe("ltr");
    expect(pre?.textContent).toContain('"instanceType": "u1.medium"');
    await user.click(screen.getByRole("button", { name: "Create virtual machine" }));
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/p/web/vms/new-vm"));
    const post = api.calls.find((call) => call.method === "POST" && call.path.endsWith("/virtualmachines"));
    expect(post?.url).toContain("fieldManager=bedrock-panel");
    expect(post?.body).toEqual(
      buildDiskVirtualMachine(
        { name: "new-vm", imageName: "ubuntu-24-04", imageScope: "cluster", instanceType: "u1.medium", customSize: false, cores: null, memoryMiB: null, preference: "ubuntu", rootDiskGi: 20, storageClass: "", extraVolumes: [], subnetRef: "default", securityGroups: [], sshKeys: [], userData: "", running: true },
        { namespace: "acme-web" },
      ),
    );
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

  it("TestLeavingWarnsOnUnsavedChanges", async () => {
    const user = userEvent.setup();
    renderWizard(<VmWizard />, path);
    await user.click(await screen.findByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/p/web/vms"));
  });

  it("asks before discarding typed values", async () => {
    const user = userEvent.setup();
    renderWizard(<VmWizard />, path);
    await user.type(await screen.findByLabelText("Name"), "typed");
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
    await user.type(screen.getByLabelText("Name"), "new-vm");
    await next(user);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("heading", { level: 2, name: "Image" })));
    expect(within(steps).getAllByRole("listitem")[1]?.getAttribute("aria-current")).toBe("step");
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
