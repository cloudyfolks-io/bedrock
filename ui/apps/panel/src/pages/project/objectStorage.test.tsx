import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { Check, KubeList } from "../../api/types";
import buckets from "../../fixtures/objectstoragebuckets.json";
import users from "../../fixtures/objectstorageusers.json";
import { definitionFor } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { validateBucket } from "../../resources/validators";
import type { FakeRoute } from "../../test/fakeApi";
import { openApiRoutes } from "../../test/openapi";
import { renderPanel } from "../../test/renderPanel";
import ObjectUsersPage from "./ObjectUsersPage";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const ns = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web";
const secretPath = "/api/v1/namespaces/acme-web/secrets/object-user-app";

function secret(extra: Record<string, string> = {}) {
  const encode = (value: string) => btoa(value);
  return { metadata: { name: "object-user-app", namespace: "acme-web" }, data: { AccessKey: encode("AKIA123"), SecretKey: encode("s3cr3t-value"), Endpoint: encode("https://s3.lab.example.test"), ...extra } };
}

function renderUsers(allow: (check: Check) => boolean = () => true, api: Record<string, FakeRoute> = {}) {
  return renderPanel(
    <Routes>
      <Route path="/p/:project/object-users" element={<ObjectUsersPage />} />
    </Routes>,
    { route: "/p/web/object-users", allow, api: { ...openApiRoutes(), "GET /proxy/v1/projects": { body: projects }, [`GET ${ns}/objectstorageusers`]: { body: users as unknown as KubeList }, ...api } },
  );
}

describe("Buckets", () => {
  it("TestPolicyJsonIsValidated", () => {
    expect(validateBucket({ policy: '{"Version":"2012-10-17"}' })).toEqual([]);
    expect(validateBucket({})).toEqual([]);
    expect(validateBucket({ policy: "{oops" })[0]?.field).toBe("spec.policy");
  });

  it("lists buckets with their name, endpoint, quota and usage", async () => {
    const user = userEvent.setup();
    const def = definitionFor("objectstoragebuckets");
    if (!def) {
      throw new Error("missing definition");
    }
    renderPanel(
      <Routes>
        <Route path="/p/:project/buckets" element={<ResourceListPage def={def} />} />
      </Routes>,
      { route: "/p/web/buckets", api: { ...openApiRoutes(), "GET /proxy/v1/projects": { body: projects }, [`GET ${ns}/objectstoragebuckets`]: { body: buckets as unknown as KubeList } } },
    );
    const row = (await screen.findByRole("link", { name: "assets" })).closest("tr") as HTMLElement;
    const ltr = Array.from(row.querySelectorAll('bdi[dir="ltr"]')).map((element) => element.textContent);
    expect(ltr).toEqual(["acme-web-assets", "https://s3.lab.example.test"]);
    expect(within(row).getByText("100 GiB")).toBeTruthy();
    expect(within(row).getByText("5 GiB")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(await screen.findByRole("textbox", { name: /bucketName/ }, { timeout: 15000 })).toBeTruthy();
    expect(screen.queryByRole("combobox", { name: /userRef/ })).toBeNull();
  });
});

describe("ObjectUsersPage", () => {
  it("lists the permission, the buckets in scope and the suspended flag", async () => {
    renderUsers();
    const app = (await screen.findByRole("link", { name: "app" })).closest("tr") as HTMLElement;
    expect(within(app).getByText("readWrite")).toBeTruthy();
    expect(within(app).getByText("2")).toBeTruthy();
    expect(within(app).queryByText("✓")).toBeNull();
    const ci = (await screen.findByRole("link", { name: "ci" })).closest("tr") as HTMLElement;
    expect(within(ci).getByText("read")).toBeTruthy();
    expect(within(ci).getByText("✓")).toBeTruthy();
  });

  it("shows credentials only when reading secrets is allowed", async () => {
    const user = userEvent.setup();
    const denied = renderUsers((check) => check.resource !== "secrets");
    await user.click(await screen.findByRole("button", { name: "Actions for app" }));
    expect(screen.queryByRole("menuitem", { name: "Show credentials" })).toBeNull();
    denied.unmount();
    renderUsers(() => true, { [`GET ${secretPath}`]: { body: secret() } });
    await user.click(await screen.findByRole("button", { name: "Actions for app" }));
    expect(await screen.findByRole("menuitem", { name: "Show credentials" })).toBeTruthy();
  });
});

describe("CredentialsDialog", () => {
  it("TestCredentialsAreFetchedOnDemand and the secret is masked until revealed", async () => {
    const user = userEvent.setup();
    const { api } = renderUsers(() => true, { [`GET ${secretPath}`]: { body: secret({ Binary: btoa(String.fromCharCode(255, 254, 253)) }) } });
    expect(api.calls.some((call) => call.path === secretPath)).toBe(false);
    await user.click(await screen.findByRole("button", { name: "Actions for app" }));
    await user.click(await screen.findByRole("menuitem", { name: "Show credentials" }));
    expect(await screen.findByText("AKIA123")).toBeTruthy();
    expect(screen.getByText("https://s3.lab.example.test")).toBeTruthy();
    expect(screen.queryByText("s3cr3t-value")).toBeNull();
    expect(screen.queryByText("Binary")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Show SecretKey" }));
    expect(screen.getByText("s3cr3t-value")).toBeTruthy();
    expect(api.calls.filter((call) => call.path === secretPath)).toHaveLength(1);
  });

  it("TestCredentialsAreNotCached and never in URLs or storage", async () => {
    const user = userEvent.setup();
    localStorage.clear();
    sessionStorage.clear();
    const { api, queryClient } = renderUsers(() => true, { [`GET ${secretPath}`]: { body: secret() } });
    await user.click(await screen.findByRole("button", { name: "Actions for app" }));
    await user.click(await screen.findByRole("menuitem", { name: "Show credentials" }));
    await screen.findByText("AKIA123");
    await user.click(screen.getByRole("button", { name: "Show SecretKey" }));
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByText("AKIA123")).toBeNull());
    expect(document.body.textContent).not.toContain("s3cr3t-value");
    await waitFor(() => expect(queryClient.getQueryCache().findAll({ queryKey: ["credentials"] })).toHaveLength(0));
    expect(JSON.stringify(Object.entries(localStorage))).not.toContain("s3cr3t");
    expect(JSON.stringify(Object.entries(sessionStorage))).not.toContain("s3cr3t");
    for (const call of api.calls) {
      expect(call.url).not.toContain("s3cr3t");
    }
  });

  it("TestCredentialsFailureShowsMessage", async () => {
    const user = userEvent.setup();
    renderUsers(() => true, { [`GET ${secretPath}`]: { status: 404, body: { kind: "Status", reason: "NotFound", message: "gone" } } });
    await user.click(await screen.findByRole("button", { name: "Actions for app" }));
    await user.click(await screen.findByRole("menuitem", { name: "Show credentials" }));
    expect(await screen.findByText("The credentials did not load.")).toBeTruthy();
  });
});
