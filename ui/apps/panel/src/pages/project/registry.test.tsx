import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { Check, KubeList } from "../../api/types";
import credentials from "../../fixtures/registrycredentials.json";
import registries from "../../fixtures/registries.json";
import { definitionFor } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import type { FakeRoute } from "../../test/fakeApi";
import { openApiRoutes } from "../../test/openapi";
import { renderPanel } from "../../test/renderPanel";
import RegistryCredentialsPage from "./RegistryCredentialsPage";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const ns = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web";
const secretPath = "/api/v1/namespaces/acme-web/secrets/ci-docker";

function loginSecret() {
  const encode = (value: string) => btoa(value);
  const config = JSON.stringify({ auths: { "registry.lab.example.test": { username: "robot$acme-images+ci", password: "pa55-w0rd-value", auth: "x" } } });
  return {
    metadata: { name: "ci-docker", namespace: "acme-web" },
    data: {
      ".dockerconfigjson": encode(config),
      host: encode("registry.lab.example.test"),
      username: encode("robot$acme-images+ci"),
      password: encode("pa55-w0rd-value"),
    },
  };
}

function renderCredentials(allow: (check: Check) => boolean = () => true, api: Record<string, FakeRoute> = {}) {
  return renderPanel(
    <Routes>
      <Route path="/p/:project/registry-credentials" element={<RegistryCredentialsPage />} />
    </Routes>,
    { route: "/p/web/registry-credentials", allow, api: { ...openApiRoutes(), "GET /proxy/v1/projects": { body: projects }, [`GET ${ns}/registrycredentials`]: { body: credentials as unknown as KubeList }, ...api } },
  );
}

describe("Registries", () => {
  it("lists the host, the quota, the usage and the repositories", async () => {
    const def = definitionFor("registries");
    if (!def) {
      throw new Error("missing definition");
    }
    renderPanel(
      <Routes>
        <Route path="/p/:project/registries" element={<ResourceListPage def={def} />} />
      </Routes>,
      { route: "/p/web/registries", api: { ...openApiRoutes(), "GET /proxy/v1/projects": { body: projects }, [`GET ${ns}/registries`]: { body: registries as unknown as KubeList } } },
    );
    const row = (await screen.findByRole("link", { name: "images" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("registry.lab.example.test")).toBeTruthy();
    expect(within(row).getByText("10 GiB")).toBeTruthy();
    expect(within(row).getByText("2 GiB")).toBeTruthy();
    expect(within(row).getByText("3")).toBeTruthy();
    const taken = (await screen.findByRole("link", { name: "taken" })).closest("tr") as HTMLElement;
    expect(within(taken).getByText("0 GiB")).toBeTruthy();
  });

  it("offers a create form with the project name", async () => {
    const user = userEvent.setup();
    const def = definitionFor("registries");
    if (!def) {
      throw new Error("missing definition");
    }
    renderPanel(
      <Routes>
        <Route path="/p/:project/registries" element={<ResourceListPage def={def} />} />
      </Routes>,
      { route: "/p/web/registries", api: { ...openApiRoutes(), "GET /proxy/v1/projects": { body: projects }, [`GET ${ns}/registries`]: { body: registries as unknown as KubeList } } },
    );
    await user.click(await screen.findByRole("button", { name: "Create" }));
    expect(await screen.findByRole("textbox", { name: /name/ }, { timeout: 15000 })).toBeTruthy();
  });
});

describe("RegistryCredentialsPage", () => {
  it("lists the registry, the permission, the login and the Secret", async () => {
    renderCredentials();
    const ci = (await screen.findByRole("link", { name: "ci" })).closest("tr") as HTMLElement;
    expect(within(ci).getByText("images")).toBeTruthy();
    expect(within(ci).getByText("pushPull")).toBeTruthy();
    expect(within(ci).getByText("robot$acme-images+ci")).toBeTruthy();
    expect(within(ci).getByText("ci-docker")).toBeTruthy();
    const waiting = (await screen.findByRole("link", { name: "waiting" })).closest("tr") as HTMLElement;
    expect(within(waiting).getByText("pull")).toBeTruthy();
  });

  it("shows the login only when reading Secrets is allowed and a Secret exists", async () => {
    const user = userEvent.setup();
    const denied = renderCredentials((check) => check.resource !== "secrets");
    await user.click(await screen.findByRole("button", { name: "Actions for ci" }));
    expect(screen.queryByRole("menuitem", { name: "Show login" })).toBeNull();
    denied.unmount();
    renderCredentials(() => true, { [`GET ${secretPath}`]: { body: loginSecret() } });
    await user.click(await screen.findByRole("button", { name: "Actions for ci" }));
    expect(await screen.findByRole("menuitem", { name: "Show login" })).toBeTruthy();
    await user.keyboard("{Escape}");
    await user.click(await screen.findByRole("button", { name: "Actions for waiting" }));
    expect(screen.queryByRole("menuitem", { name: "Show login" })).toBeNull();
  });

  it("masks the password and never shows the docker config", async () => {
    const user = userEvent.setup();
    const { api } = renderCredentials(() => true, { [`GET ${secretPath}`]: { body: loginSecret() } });
    expect(api.calls.some((call) => call.path === secretPath)).toBe(false);
    await user.click(await screen.findByRole("button", { name: "Actions for ci" }));
    await user.click(await screen.findByRole("menuitem", { name: "Show login" }));
    expect(await screen.findByText("registry.lab.example.test")).toBeTruthy();
    expect(screen.getAllByText("robot$acme-images+ci").length).toBeGreaterThan(0);
    expect(screen.queryByText(".dockerconfigjson")).toBeNull();
    expect(document.body.textContent).not.toContain("pa55-w0rd-value");
    expect(document.body.textContent).not.toContain("auths");
    await user.click(screen.getByRole("button", { name: "Show password" }));
    expect(screen.getByText("pa55-w0rd-value")).toBeTruthy();
    expect(api.calls.filter((call) => call.path === secretPath)).toHaveLength(1);
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByText("pa55-w0rd-value")).toBeNull());
  });

  it("shows a message when the Secret does not load", async () => {
    const user = userEvent.setup();
    renderCredentials(() => true, { [`GET ${secretPath}`]: { status: 404, body: { kind: "Status", reason: "NotFound", message: "gone" } } });
    await user.click(await screen.findByRole("button", { name: "Actions for ci" }));
    await user.click(await screen.findByRole("menuitem", { name: "Show login" }));
    expect(await screen.findByText("The credentials did not load.")).toBeTruthy();
  });
});
