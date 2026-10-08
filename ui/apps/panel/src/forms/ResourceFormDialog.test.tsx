import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it, vi } from "vitest";
import type { KubeObject } from "../api/types";
import subnets from "../fixtures/subnets.json";
import { definitionFor, type ResourceDef } from "../resources/registry";
import { openApiRoutes } from "../test/openapi";
import { renderPanel } from "../test/renderPanel";
import type { FakeRoute } from "../test/fakeApi";
import { CreateResourceDialog, EditResourceDialog } from "./ResourceFormDialog";

function subnetDefinition(): ResourceDef {
  const found = definitionFor("subnets");
  if (!found) {
    throw new Error("missing definition");
  }
  return found;
}

const def = subnetDefinition();
const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const subnetPath = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/subnets";
const existing = (subnets as unknown as { items: KubeObject[] }).items[0] as KubeObject;

function routes(extra: Record<string, FakeRoute> = {}): Record<string, FakeRoute> {
  return { ...openApiRoutes(), "GET /proxy/v1/projects": { body: projects }, ...extra };
}

function renderCreate(extra: Record<string, FakeRoute>, onClose = vi.fn()) {
  const view = renderPanel(
    <Routes>
      <Route path="/p/:project/subnets" element={<CreateResourceDialog def={def} onClose={onClose} />} />
    </Routes>,
    { route: "/p/web/subnets", api: routes(extra) },
  );
  return { ...view, onClose };
}

function renderEdit(extra: Record<string, FakeRoute>, onClose = vi.fn()) {
  const view = renderPanel(
    <Routes>
      <Route path="/p/:project/subnets" element={<EditResourceDialog def={def} object={existing} onClose={onClose} />} />
    </Routes>,
    { route: "/p/web/subnets", api: routes(extra) },
  );
  return { ...view, onClose };
}

async function fillCreate(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.type(await screen.findByLabelText("Name"), name);
  await user.type(screen.getByLabelText(/^cidr/), "10.1.0.0/24");
  await user.type(screen.getByLabelText(/^networkRef/), "acme");
}

describe("CreateResourceDialog", () => {
  it("TestCreatePostsNameAndSpec", async () => {
    const user = userEvent.setup();
    const { api, onClose } = renderCreate({ [`POST ${subnetPath}`]: { status: 201, body: {} } });
    await fillCreate(user, "Bad_Name");
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(await screen.findByText(/lowercase letters/)).toBeTruthy();
    expect(api.calls.some((call) => call.method === "POST")).toBe(false);
    await user.clear(screen.getByLabelText("Name"));
    await user.type(screen.getByLabelText("Name"), "net-a");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const post = api.calls.find((call) => call.method === "POST");
    expect(post?.url).toContain("fieldManager=bedrock-panel");
    expect(post?.body).toMatchObject({
      apiVersion: "bedrock.cloudyfolks.io/v1alpha1",
      kind: "Subnet",
      metadata: { name: "net-a", namespace: "acme-web" },
      spec: { cidr: "10.1.0.0/24", networkRef: "acme" },
    });
    expect(await screen.findByText("Created net-a")).toBeTruthy();
  });

  it("TestValidationCausesAreMapped", async () => {
    const user = userEvent.setup();
    renderCreate({
      [`POST ${subnetPath}`]: {
        status: 422,
        body: { kind: "Status", reason: "Invalid", message: "bad", details: { causes: [{ field: "spec.cidr", message: "overlaps" }, { field: "spec.nothing", message: "unknown" }] } },
      },
    });
    await fillCreate(user, "net-a");
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(await screen.findByText("overlaps")).toBeTruthy();
    expect(screen.getByText("spec.nothing: unknown")).toBeTruthy();
  });

  it("TestForbiddenWriteShowsToast", async () => {
    const user = userEvent.setup();
    const { onClose } = renderCreate({ [`POST ${subnetPath}`]: { status: 403, body: { kind: "Status", reason: "Forbidden", message: "forbidden: no create" } } });
    await fillCreate(user, "net-a");
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(await screen.findByText("You are not allowed to do this")).toBeTruthy();
    expect(screen.getByRole("alert", { hidden: true }).textContent).toContain("forbidden: no create");
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  it("shows an exists error on the name field", async () => {
    const user = userEvent.setup();
    renderCreate({ [`POST ${subnetPath}`]: { status: 409, body: { kind: "Status", reason: "AlreadyExists", message: "exists" } } });
    await fillCreate(user, "net-a");
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(await screen.findByText("An object with this name already exists")).toBeTruthy();
  });
});

describe("EditResourceDialog", () => {
  const getPath = `GET ${subnetPath}/default`;
  const latest = { ...existing, metadata: { ...existing.metadata, resourceVersion: "200" } };

  it("TestEditReplacesWithResourceVersion", async () => {
    const user = userEvent.setup();
    const { api, onClose } = renderEdit({ [getPath]: { body: latest }, [`PUT ${subnetPath}/default`]: { body: latest } });
    const gateway = await screen.findByLabelText(/^gateway/);
    await user.clear(gateway);
    await user.type(gateway, "10.100.0.254");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const put = api.calls.find((call) => call.method === "PUT");
    const body = put?.body as KubeObject;
    expect(body.metadata.resourceVersion).toBe("200");
    expect(body).not.toHaveProperty("status");
    expect(body.spec).toMatchObject({ gateway: "10.100.0.254", cidr: "10.100.0.0/24" });
    expect(put?.url).toContain("fieldManager=bedrock-panel");
  });

  it("makes immutable fields read only", async () => {
    renderEdit({ [getPath]: { body: latest } });
    expect(((await screen.findByLabelText(/^cidr/)) as HTMLInputElement).readOnly).toBe(true);
    expect(((screen.getByLabelText(/^gateway/)) as HTMLInputElement).readOnly).toBe(false);
  });

  it("TestConflictAsksForReload", async () => {
    const user = userEvent.setup();
    let reads = 0;
    renderEdit({
      [getPath]: () => {
        reads += 1;
        return { body: { ...latest, metadata: { ...latest.metadata, resourceVersion: String(200 + reads) } } };
      },
      [`PUT ${subnetPath}/default`]: { status: 409, body: { kind: "Status", reason: "Conflict", message: "the object has been modified" } },
    });
    const gateway = await screen.findByLabelText(/^gateway/);
    await user.clear(gateway);
    await user.type(gateway, "10.100.0.200");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/Someone else changed this object/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(screen.queryByText(/Someone else changed this object/)).toBeNull());
    expect(reads).toBe(2);
    expect((screen.getByLabelText(/^gateway/) as HTMLInputElement).value).toBe("10.100.0.200");
  });
});
