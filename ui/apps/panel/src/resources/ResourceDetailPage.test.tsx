import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it, vi } from "vitest";
import type { Check, KubeList, KubeObject } from "../api/types";
import { RegistryCredentialDetailPage } from "../pages/project/RegistryCredentialsPage";
import { axeViolations } from "../test/axe";
import { createFakeSockets } from "../test/fakeSocket";
import type { FakeRoute } from "../test/fakeApi";
import { virtualMachines } from "../test/fixtures";
import { renderPanel } from "../test/renderPanel";
import { detailFacts, specificationJSON } from "./detailFacts";
import { definitionFor, definitions } from "./registry";
import { ResourceDetailPage } from "./ResourceDetailPage";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const hello = { type: "hello", expiresAt: "2026-10-09T00:00:00Z", limits: { maxSubscriptions: 64 } };
const fixtures = import.meta.glob("../fixtures/*.json", { eager: true, import: "default" }) as Record<string, KubeList>;
const root = "/apis/bedrock.cloudyfolks.io/v1alpha1";
const secretPath = "/api/v1/namespaces/acme-web/secrets/ci-docker";

function fixture(plural: string, name?: string): KubeObject {
  const items = fixtures[`../fixtures/${plural}.json`]?.items;
  const object = name ? items?.find((item) => item.metadata.name === name) : items?.[0];
  if (!object) throw new Error(`Missing detail fixture: ${plural}/${name ?? ""}`);
  return object;
}

function web1() {
  const found = virtualMachines().items.find((item) => item.metadata.name === "web-1");
  if (!found) {
    throw new Error("fixture");
  }
  return found;
}

function renderDetail(options: {
  allow?: (check: Check) => boolean;
  sockets?: ReturnType<typeof createFakeSockets>;
  body?: unknown;
  status?: number;
  plural?: string;
  name?: string;
  api?: Record<string, FakeRoute>;
} = {}) {
  const { plural = "virtualmachines", name = plural === "virtualmachines" ? "web-1" : undefined } = options;
  const def = definitionFor(plural);
  if (!def) {
    throw new Error("missing definition");
  }
  const object = fixture(plural, name);
  const scope = def.perspective === "project" ? "/p/:project" : "/admin";
  const base = def.perspective === "project" ? "/p/web" : "/admin";
  const apiBase = def.scope === "Namespaced" ? `${root}/namespaces/acme-web` : root;
  const element = plural === "registrycredentials" ? <RegistryCredentialDetailPage /> : <ResourceDetailPage def={def} />;
  return renderPanel(
    <Routes>
      <Route path={`${scope}/${def.path}/:name`} element={element} />
    </Routes>,
    {
      route: `${base}/${def.path}/${object.metadata.name}`,
      ...(options.allow ? { allow: options.allow } : {}),
      ...(options.sockets ? { socket: options.sockets } : {}),
      api: {
        "GET /proxy/v1/projects": { body: projects },
        [`GET ${apiBase}/${plural}/${object.metadata.name}`]: { status: options.status ?? 200, body: options.body ?? object },
        ...options.api,
      },
    },
  );
}

describe("ResourceDetailPage", () => {
  it("shows health in the header, overview first, and full left-to-right spec on demand", async () => {
    const user = userEvent.setup();
    const { container } = renderDetail();
    const heading = await screen.findByRole("heading", { level: 1, name: "web-1" });
    const breadcrumb = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(breadcrumb.querySelector('[aria-current="page"]')?.textContent).toContain("web-1");
    expect(within(heading.closest("header")!).getByRole("status", { name: "Status" }).textContent).toBe("Running");
    const overview = screen.getByRole("region", { name: "Overview" });
    expect(within(overview).getByText("u1.medium")).toBeTruthy();
    const conditions = screen.getByRole("table", { name: "Conditions" });
    expect(overview.compareDocumentPosition(conditions) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(within(conditions).getByRole("cell", { name: "Ready" })).toBeTruthy();
    const summary = screen.getByText("Specification", { selector: "summary" });
    const disclosure = summary.closest("details")!;
    expect(disclosure.open).toBe(false);
    summary.focus();
    expect(document.activeElement).toBe(summary);
    await user.click(summary);
    expect(disclosure.open).toBe(true);
    const pre = disclosure.querySelector("pre");
    expect(pre?.getAttribute("dir")).toBe("ltr");
    expect(pre?.tabIndex).toBe(0);
    expect(JSON.parse(pre?.textContent ?? "")).toEqual(web1().spec);
    expect(await axeViolations(container)).toEqual([]);
  });

  it("updates from the stream and shows a deleted object", async () => {
    const sockets = createFakeSockets();
    renderDetail({ sockets });
    await screen.findByRole("heading", { level: 1, name: "web-1" });
    await waitFor(() => expect(sockets.sockets).toHaveLength(1));
    const socket = sockets.latest();
    act(() => {
      socket.open();
      socket.receive(hello);
    });
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(1));
    const id = socket.framesOf("subscribe")[0]?.id;
    expect(screen.getAllByText("Running").length).toBeGreaterThan(0);
    act(() => socket.receive({ type: "event", id, event: "MODIFIED", resourceVersion: "101", object: { ...web1(), status: { ...web1().status, phase: "Stopping" } } }));
    expect(await screen.findByText("Stopping")).toBeTruthy();
    act(() => socket.receive({ type: "event", id, event: "DELETED", resourceVersion: "102", object: web1() }));
    expect(await screen.findByText("This object was deleted")).toBeTruthy();
  });

  it("shows action buttons that follow the checks with the object name", async () => {
    const user = userEvent.setup();
    const { api } = renderDetail({ allow: (check) => check.subresource === "stop" && check.name === "web-1" });
    const heading = await screen.findByRole("heading", { level: 1, name: "web-1" });
    const stop = await within(heading.closest("header")!).findByRole("button", { name: "Stop" });
    expect(screen.queryByRole("button", { name: "Start" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
    const authz = api.calls.filter((call) => call.path === "/proxy/v1/authz");
    expect(authz).toHaveLength(1);
    await user.click(stop);
    expect(await screen.findByRole("alertdialog")).toBeTruthy();
  });

  it("shows no access for a forbidden object", async () => {
    renderDetail({ status: 403, body: { error: "forbidden" } });
    expect(await screen.findByText("You cannot view this")).toBeTruthy();
  });

  it.each(definitions.map(({ plural }) => plural))("gives %s structured domain facts from its fixture", (plural) => {
    expect(detailFacts(plural, fixture(plural), "en").filter(({ values }) => values.length).length).toBeGreaterThan(0);
  });

  it("shows and copies the database address with its real port, but does not copy credential data", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText");
    renderDetail({ plural: "databases" });
    const overview = await screen.findByRole("region", { name: "Overview" });
    expect(within(overview).getByText("10.100.0.20:5432").getAttribute("dir")).toBe("ltr");
    expect(within(overview).getByRole("link", { name: "default" }).getAttribute("href")).toBe("/p/web/subnets/default");
    expect(screen.queryByText("orders-admin")).toBeNull();
    const copy = within(overview).getByRole("button", { name: "Copy Address and port" });
    copy.focus();
    await user.keyboard("{Enter}");
    expect(writeText).toHaveBeenCalledWith("10.100.0.20:5432");
    expect(await screen.findByText("Copied to clipboard")).toBeTruthy();
    writeText.mockRejectedValueOnce(new Error("Permission denied"));
    await user.click(copy);
    expect(await screen.findByText("Could not copy to clipboard")).toBeTruthy();
  });

  it.each([
    ["objectstoragebuckets", "https://s3.lab.example.test", "5 GiB"],
    ["registries", "registry.lab.example.test", "2 GiB"],
    ["containers", "10.100.0.11", "2/2"],
    ["builds", "registry.example.test/acme/shop:1", "Result image"],
    ["subnets", "10.100.0.0/24", "10.100.0.1"],
    ["elasticips", "203.0.113.10", "web-1"],
    ["loadbalancers", "TCP 443 · web-1:8443", "web-ip"],
    ["volumes", "web-1", "100 GiB"],
  ])("puts the operational data for %s before its spec", async (plural, first, second) => {
    renderDetail({ plural });
    const overview = within(await screen.findByRole("region", { name: "Overview" }));
    expect(overview.getAllByText(first).length).toBeGreaterThan(0);
    expect(overview.getByText(second)).toBeTruthy();
    expect(screen.getByText("Specification", { selector: "summary" }).closest("details")?.open).toBe(false);
  });

  it("omits inline secrets from advanced JSON, without removing safe references", async () => {
    const user = userEvent.setup();
    const object = fixture("containers");
    renderDetail({ plural: "containers", body: {
      ...object,
      spec: { ...object.spec, env: [{ name: "PG", value: "inline-test-secret" }], args: ["arg-test-secret"], password: "password-test-secret" },
    } });
    const summary = await screen.findByText("Specification", { selector: "summary" });
    await user.click(summary);
    const spec = JSON.parse(summary.closest("details")?.querySelector("code")?.textContent ?? "");
    expect(spec).toMatchObject({ image: object.spec?.image, env: "[redacted]", args: "[redacted]", password: "[redacted]" });
    expect(document.body.textContent).not.toMatch(/(inline|arg|password)-test-secret/);

    expect(JSON.parse(specificationJSON("builds", fixture("builds").spec))).toEqual(fixture("builds").spec);
    expect(JSON.parse(specificationJSON("images", { source: { http: { url: "https://user:pass@example.test/image?signature=private" } } })))
      .toEqual({ source: { http: { url: "https://example.test/image" } } });
  });

  it("offers registry login in the detail header and fetches credentials only after opening", async () => {
    const user = userEvent.setup();
    const { api } = renderDetail({ plural: "registrycredentials", api: { [`GET ${secretPath}`]: { body: {
      metadata: { name: "ci-docker", namespace: "acme-web" },
      data: { host: btoa("registry.lab.example.test"), password: btoa("test-registry-password"), ".dockerconfigjson": btoa("hidden-registry-config") },
    } } } });
    const heading = await screen.findByRole("heading", { level: 1, name: "ci" });
    const button = await within(heading.closest("header")!).findByRole("button", { name: "Show login" });
    expect(screen.getByRole("link", { name: "images" }).getAttribute("href")).toBe("/p/web/registries/images");
    expect(api.calls.some(({ path }) => path === secretPath)).toBe(false);
    await user.click(button);
    const dialog = within(await screen.findByRole("dialog", { name: "Access credentials" }));
    expect(await dialog.findByText("registry.lab.example.test")).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/test-registry-password|hidden-registry-config|\.dockerconfigjson/);
    expect(dialog.queryByRole("button", { name: "Copy .dockerconfigjson" })).toBeNull();
    await user.click(dialog.getByRole("button", { name: "Show password" }));
    expect(dialog.getByText("test-registry-password")).toBeTruthy();
    expect(api.calls.filter(({ path }) => path === secretPath)).toHaveLength(1);
  });

  it.each([
    ["ci", (check: Check) => check.resource !== "secrets"],
    ["waiting", () => true],
  ] as const)("does not offer registry login without permission or a Secret: %s", async (name, allow) => {
    const { api } = renderDetail({ plural: "registrycredentials", name, allow });
    await screen.findByRole("heading", { level: 1, name });
    await screen.findByRole("button", { name: "Delete" });
    await waitFor(() => expect(api.calls.some(({ path, body }) => path === "/proxy/v1/authz" && JSON.stringify(body).includes('"resource":"secrets"'))).toBe(true));
    expect(screen.queryByRole("button", { name: "Show login" })).toBeNull();
    expect(api.calls.some(({ path }) => path === secretPath)).toBe(false);
  });
});
