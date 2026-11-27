import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import type { KubeList, KubeObject } from "../api/types";
import { definitions, definitionsFor, primaryDefinitionsFor, type Perspective, type ResourceDef } from "../resources/registry";
import { panelRoutes } from "../shell/routes";
import { storedKeys } from "../storage";
import type { FakeReply, FakeRoute } from "../test/fakeApi";
import { renderPanel } from "../test/renderPanel";

const fixtures = import.meta.glob("../fixtures/*.json", { eager: true, import: "default" }) as Record<string, KubeList>;
const project = { name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" };
const projectApi = "/proxy/v1/projects";
const projectBase = `/p/${project.name}`;
const networksApi = `${projectApi}/${project.name}/networks`;
const root = "/apis/bedrock.cloudyfolks.io/v1alpha1";
const scoped = `${root}/namespaces/${project.namespace}`;
const empty = { metadata: {}, items: [] };
const unavailable = { status: 503, body: { kind: "Status", reason: "ServiceUnavailable" } };
const missing = { status: 404, body: { kind: "Status", reason: "NotFound" } };
const forbidden = { status: 403, body: { kind: "Status", reason: "Forbidden" } };
const ready = { timeout: 5000 };
const projectSession = {
  admin: false,
  user: { name: "alex", displayName: "Alex", email: "alex@example.test", groups: ["acme-members"] },
};

function fixture(plural: string): KubeList {
  const list = fixtures[`../fixtures/${plural}.json`];
  if (!list?.items.length) throw new Error(`Missing populated fixture: ${plural}`);
  return list;
}

function collection(def: ResourceDef, namespace = true) {
  return `${def.scope === "Namespaced" && namespace ? scoped : root}/${def.plural}`;
}

function sample(def: ResourceDef): KubeObject {
  const { items } = fixture(def.plural);
  const item = def.plural === "virtualmachines" ? items.find(({ metadata }) => metadata.name === "web-1") : items[0];
  if (!item) throw new Error(`Missing route sample: ${def.plural}`);
  return item;
}

function show(route: string, perspective: Perspective, override: Record<string, FakeRoute> = {}, previous?: string) {
  const api: Record<string, FakeRoute> = {
    [`GET ${projectApi}`]: { body: { projects: [project] } },
    [`GET ${networksApi}`]: { body: { ...fixture("networks"), items: fixture("networks").items.filter(({ spec }) => spec?.tenantRef === project.tenant) } },
    "GET /proxy/v1/metrics/range": { body: { status: "success", data: { resultType: "matrix", result: [] } } },
    "GET /apis/instancetype.kubevirt.io/v1beta1/virtualmachineclusterinstancetypes": { body: empty },
  };
  for (const def of definitions) {
    const list = fixture(def.plural);
    api[`GET ${collection(def)}`] = { body: list };
    api[`GET ${collection(def, false)}`] = { body: list };
    for (const item of list.items) {
      api[`GET ${collection(def)}/${item.metadata.name}`] = { body: item };
    }
  }
  return renderPanel(panelRoutes, {
    route,
    ...(previous ? { initialEntries: [previous, route] } : {}),
    ...(perspective === "project" ? { session: projectSession } : {}),
    allow: (check) => perspective === "admin" || check.namespace === project.namespace || check.resource === "networks",
    api: { ...api, ...override },
  });
}

const main = () => within(screen.getByRole("main"));
const heading = (name: string) => main().findByRole("heading", { level: 1, name }, ready);

beforeEach(() => {
  for (const key of Object.values(storedKeys)) localStorage.removeItem(key);
});

for (const perspective of ["admin", "project"] as const) {
  const base = perspective === "admin" ? "/admin" : projectBase;

  describe(`${perspective} collection and detail routes`, () => {
    for (const def of primaryDefinitionsFor(perspective)) {
      const listRoute = `${base}/${def.path}`;
      const item = sample(def);
      const name = item.metadata.name;
      const detailRoute = `${listRoute}/${name}`;

      it(`loads ${listRoute} with fixture data`, async () => {
        const { api } = show(listRoute, perspective);
        if (def.path === "upgrades") {
          expect(await main().findByRole("list", { name: "Upgrade phases" }, ready)).toBeTruthy();
        } else if (def.path === "addons") {
          expect(await main().findByRole("region", { name }, ready)).toBeTruthy();
        } else if (def.path === "settings") {
          expect(await main().findByText(name, { selector: "bdi" }, ready)).toBeTruthy();
        } else {
          const link = await main().findByRole("link", { name }, ready);
          expect(link.closest("tbody")).not.toBeNull();
          expect(link.getAttribute("href")).toBe(detailRoute);
        }
        if (def.path === "backups") {
          expect(await main().findByText("bk-001", {}, ready)).toBeTruthy();
          expect(await main().findByText("web-1-restore", {}, ready)).toBeTruthy();
        }
        if (def.path === "vms") {
          expect((await main().findByRole("link", { name: "Create" }, ready)).getAttribute("href")).toBe(`${listRoute}/new`);
          expect(main().getByRole("link", { name: "Install from ISO" }).getAttribute("href")).toBe(`${listRoute}/new-iso`);
        }
        const path = def.plural === "networks" ? networksApi : collection(def);
        expect(api.calls.some((call) => call.method === "GET" && call.path === path)).toBe(true);
        if (def.scope === "Namespaced") {
          expect(api.calls.some((call) => call.path === collection(def, false))).toBe(false);
        }
      });

      it(`loads ${detailRoute} with fixture data`, async () => {
        const { api } = show(detailRoute, perspective);
        if (def.path === "tenants") {
          await heading("Acme Corp");
          const table = await main().findByRole("table", { name: "Projects" }, ready);
          expect(within(table).getByText("acme-db")).toBeTruthy();
          expect(within(table).getByRole("link", { name: "web" }).getAttribute("href")).toBe("/admin/inventory?project=web");
        } else {
          await heading(name);
          if (def.path === "hosts") {
            expect(await main().findByRole("table", { name: "Conditions" }, ready)).toBeTruthy();
          } else if (def.path === "vms") {
            expect(await main().findByText("u1.medium", {}, ready)).toBeTruthy();
          } else if (def.path === "security-groups") {
            const table = await main().findByRole("table", { name: "Ingress rules" }, ready);
            expect(within(table).getByRole("cell", { name: "10.0.0.0/8" })).toBeTruthy();
        } else {
            const sections = main().getAllByRole("heading", { level: 2 });
            expect(sections[0]?.textContent).toBe("Overview");
            const specification = await main().findByText("Specification", { selector: "summary" }, ready);
            const disclosure = specification.closest("details")!;
            expect(disclosure.open).toBe(false);
            await userEvent.setup().click(specification);
            expect(disclosure.open).toBe(true);
            expect(JSON.parse(disclosure.querySelector("code")!.textContent!)).toEqual(item.spec ?? {});
          }
        }
        expect(api.calls.some((call) => call.method === "GET" && call.path === `${collection(def)}/${name}`)).toBe(true);
        expect(Array.from(screen.getByRole("main").querySelectorAll("a")).some((link) => link.getAttribute("href") === listRoute)).toBe(true);
      });
    }
  });
}

describe("panel landing routes and drilldowns", () => {
  it.each(["/", "/admin", "/admin/overview"])("loads overview from %s as admin", async (route) => {
    show(route, "admin");
    expect((await main().findByRole("link", { name: "3 Hosts" }, ready)).getAttribute("href")).toBe("/admin/hosts");
    expect(main().getByRole("link", { name: "2 Tenants" }).getAttribute("href")).toBe("/admin/tenants");
    expect(main().getByRole("link", { name: "Resource inventory" }).getAttribute("href")).toBe("/admin/inventory");
  });

  it.each(["/", "/p", projectBase, "/admin/inventory"])("lands on project VMs from %s as a project member", async (route) => {
    const { api } = show(route, "project");
    expect((await main().findByRole("link", { name: "web-1" }, ready)).getAttribute("href")).toBe(`${projectBase}/vms/web-1`);
    expect(api.calls.some((call) => call.path === `${root}/virtualmachines`)).toBe(false);
  });

  it("follows the inventory VM link into the project namespace as admin", async () => {
    const user = userEvent.setup();
    const { api } = show("/admin/inventory", "admin");
    await heading("Resource inventory");
    const table = await main().findByRole("table", { name: "Virtual machines" }, ready);
    expect(api.calls.find((call) => call.path === `${root}/virtualmachines`)?.query.get("limit")).toBe("50");
    await user.click(within(table).getByRole("link", { name: "web-1" }));
    expect(await main().findByText("u1.medium", {}, ready)).toBeTruthy();
    expect(api.calls.some((call) => call.path === `${scoped}/virtualmachines/web-1`)).toBe(true);
  });

  it.each(definitionsFor("project").map((def) => [def.plural === "virtualmachinerestores" ? "restores" : def.path, def] as const))(
    "loads the %s inventory view",
    async (kind, def) => {
      const route = `/admin/inventory?kind=${kind}${def.scope === "Namespaced" ? "&project=web" : ""}`;
      const { api } = show(route, "admin");
      const table = await main().findByRole("table", {}, ready);
      expect(within(table).getAllByText(sample(def).metadata.name).length).toBeGreaterThan(0);
      expect(api.calls.find((call) => call.path === collection(def))?.query.get("limit")).toBe("50");
    },
  );

  it.each(["admin", "project"] as const)("loads account preferences and downloads as %s", async (perspective) => {
    show("/account", perspective);
    await heading("Account");
    expect(main().getByRole("combobox", { name: "Language" })).toBeTruthy();
    expect(main().getByRole("link", { name: "Download kubeconfig" }).getAttribute("href")).toBe("/kubeconfig");
    expect(main().getByRole("link", { name: "Download CA certificate" }).getAttribute("href")).toBe("/ca.crt");
  });
});

describe("routed tabs and wizard steps", () => {
  it("opens Host Inventory and Metrics tabs", async () => {
    const user = userEvent.setup();
    const { api } = show("/admin/hosts/host-01", "admin");
    await user.click(await main().findByRole("tab", { name: "Inventory" }, ready));
    expect(await main().findByText("/dev/nvme0n1", {}, ready)).toBeTruthy();
    await user.click(main().getByRole("tab", { name: "Metrics" }));
    expect(await main().findByRole("heading", { name: "CPU use" }, ready)).toBeTruthy();
    await waitFor(() => expect(api.calls.some((call) => call.path === "/proxy/v1/metrics/range" && call.query.get("name") === "host-01")).toBe(true));
  });

  it("deep-links to VM Disks, then opens Networks", async () => {
    const user = userEvent.setup();
    show(`${projectBase}/vms/web-1?tab=disks`, "project");
    expect((await main().findByRole("link", { name: "data-1" }, ready)).getAttribute("href")).toBe(`${projectBase}/volumes/data-1`);
    expect(main().getByRole("tab", { name: "Disks", selected: true })).toBeTruthy();
    await user.click(main().getByRole("tab", { name: "Networks" }));
    expect(await main().findByText("52:54:00:12:34:56", {}, ready)).toBeTruthy();
    expect(main().getByRole("tab", { name: "Networks", selected: true })).toBeTruthy();
  });

  it("deep-links to a stopped VM console", async () => {
    show(`${projectBase}/vms/db-1?tab=console`, "project");
    expect(await main().findByText("Start the VM to open the console", {}, ready)).toBeTruthy();
  });

  it.each([
    ["new", "Create a virtual machine", /^Image/, /ubuntu-24-04/],
    ["new-iso", "Install a virtual machine from an ISO", /Installation ISO/, /ready-iso/],
  ] as const)("opens /p/web/vms/%s and offers fixture images", async (path, title, picker, image) => {
    const user = userEvent.setup();
    const images = fixture("images");
    const installer = images.items.find(({ metadata }) => metadata.name === "installer")!;
    show(`${projectBase}/vms/${path}`, "project", {
      [`GET ${scoped}/images`]: { body: { ...images, items: [...images.items, {
        ...installer,
        metadata: { ...installer.metadata, name: "ready-iso", uid: "uid-image-ready-iso" },
        status: { phase: "Ready" },
      }] } },
    });
    await heading(title);
    await user.type(await main().findByRole("textbox", { name: "Name" }, ready), "web-1");
    await user.click(main().getByRole("button", { name: /^Next/ }));
    expect(await main().findByText("A virtual machine with this name already exists", {}, ready)).toBeTruthy();
    await user.clear(main().getByRole("textbox", { name: "Name" }));
    await user.type(main().getByRole("textbox", { name: "Name" }), "new-worker");
    await user.click(main().getByRole("button", { name: /^Next/ }));
    await user.click(await main().findByRole("combobox", { name: picker }, ready));
    expect(await screen.findByRole("option", { name: image }, ready)).toBeTruthy();
    expect(screen.queryByRole("option", { name: /installer/ })).toBeNull();
  });

  it.each(["new", "new-iso"])("guards /p/web/vms/%s against the sidebar and history Back", async (path) => {
    const user = userEvent.setup();
    const route = `${projectBase}/vms/${path}`;
    const { router } = show(route, "project", {
      [`GET ${projectApi}`]: { body: { projects: [project, { ...project, name: "db", displayName: "Database", namespace: "acme-db" }] } },
    }, "/account");
    await user.type(await main().findByRole("textbox", { name: "Name" }, ready), "draft-vm");
    await user.click(main().getByRole("button", { name: /^Next/ }));
    const nav = within(screen.getByRole("navigation", { name: "Main navigation" }));
    await user.click(nav.getByRole("link", { name: "Virtual machines" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(router.state.location.pathname).toBe(route);
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await user.click(screen.getByRole("combobox", { name: "Project" }));
    await user.click(await screen.findByRole("option", { name: "Database" }));
    await user.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Cancel" }));
    expect(router.state.location.pathname).toBe(route);
    expect(localStorage.getItem(storedKeys.project)).toBe("web");
    await user.click(main().getByRole("button", { name: /Back/ }));
    expect((main().getByRole("textbox", { name: "Name" }) as HTMLInputElement).value).toBe("draft-vm");

    await act(() => router.navigate(-1));
    expect(router.state.location.pathname).toBe(route);
    await user.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Discard" }));
    await heading("Account");
    expect(router.state.location.pathname).toBe("/account");
  });
});

describe("routed loading, missing and error states", () => {
  it("renders collection loading until the API resolves an empty list", async () => {
    let resolve!: (reply: FakeReply) => void;
    const pending = new Promise<FakeReply>((done) => { resolve = done; });
    show(`${projectBase}/containers`, "project", { [`GET ${scoped}/containers`]: () => pending });
    await heading("Containers");
    expect(main().getByText("Loading")).toBeTruthy();
    resolve({ body: empty });
    expect(await main().findByRole("heading", { name: "Nothing here yet" }, ready)).toBeTruthy();
    expect(main().queryByRole("table")).toBeNull();
  });

  it("retries a failed project list and shows its records", async () => {
    const user = userEvent.setup();
    let fail = true;
    show(`${projectBase}/databases`, "project", {
      [`GET ${scoped}/databases`]: () => fail ? unavailable : { body: fixture("databases") },
    });
    expect(await main().findByRole("heading", { name: "The list did not load" }, ready)).toBeTruthy();
    fail = false;
    await user.click(main().getByRole("button", { name: "Retry" }));
    expect(await main().findByRole("link", { name: "orders" }, ready)).toBeTruthy();
  });

  it("shows a retryable error for an unavailable admin detail", async () => {
    show("/admin/hosts/host-01", "admin", { [`GET ${root}/hosts/host-01`]: unavailable });
    expect(await main().findByText("The panel could not load this data.", {}, ready)).toBeTruthy();
    expect(main().getByRole("button", { name: "Retry" })).toBeTruthy();
    expect(main().queryByText("This object was deleted")).toBeNull();
  });

  it.each([
    ["vms/web-1", "virtualmachines/web-1"],
    ["security-groups/web", "securitygroups/web"],
    ["containers/web", "containers/web"],
  ])("shows the deletion state for /p/web/%s when the API returns 404", async (route, endpoint) => {
    show(`${projectBase}/${route}`, "project", { [`GET ${scoped}/${endpoint}`]: missing });
    expect(await main().findByText("This object was deleted", {}, ready)).toBeTruthy();
    expect(main().queryByRole("button", { name: "Retry" })).toBeNull();
  });

  describe.each([
    ["vms/web-1", "virtualmachines/web-1"],
    ["security-groups/web", "securitygroups/web"],
  ])("API errors at /p/web/%s", (route, endpoint) => {
    it.each([forbidden, unavailable])("does not label HTTP $status as a deletion", async (reply) => {
      show(`${projectBase}/${route}`, "project", { [`GET ${scoped}/${endpoint}`]: reply });
      const denied = reply.status === 403;
      expect(await main().findByText(denied ? "You cannot view this" : "The panel could not load this data.", {}, ready)).toBeTruthy();
      expect(main().queryByText("This object was deleted")).toBeNull();
      expect(Boolean(main().queryByRole("button", { name: "Retry" }))).toBe(!denied);
    });
  });

  it("distinguishes an empty project index from an unavailable project API", async () => {
    const first = show("/p", "project", { [`GET ${projectApi}`]: { body: { projects: [] } } });
    expect(await main().findByText("No projects", {}, ready)).toBeTruthy();
    first.unmount();
    show("/p", "project", { [`GET ${projectApi}`]: unavailable });
    expect(await main().findByText("The panel could not load this data.", {}, ready)).toBeTruthy();
  });

  it("does not request resources for a project whose namespace is pending", async () => {
    const { api } = show(`${projectBase}/vms`, "project", {
      [`GET ${projectApi}`]: { body: { projects: [{ ...project, namespace: "" }] } },
    });
    expect(await main().findByText("The project namespace is not ready", {}, ready)).toBeTruthy();
    expect(api.calls.some((call) => call.path.startsWith(root))).toBe(false);
  });

  it.each(["/p/missing/vms", "/missing"])("keeps unknown routes out of resource APIs: %s", async (route) => {
    const { api } = show(route, "project");
    expect(await main().findByText("Page not found", {}, ready)).toBeTruthy();
    expect(api.calls.some((call) => call.path.startsWith(root))).toBe(false);
  });
});
