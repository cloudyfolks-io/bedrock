import type { ReactElement } from "react";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { Check, KubeList, KubeObject } from "../../api/types";
import dnszones from "../../fixtures/dnszones.json";
import elasticips from "../../fixtures/elasticips.json";
import internetgateways from "../../fixtures/internetgateways.json";
import loadbalancers from "../../fixtures/loadbalancers.json";
import networks from "../../fixtures/networks.json";
import securitygroups from "../../fixtures/securitygroups.json";
import subnets from "../../fixtures/subnets.json";
import virtualmachines from "../../fixtures/virtualmachines.json";
import { definitionFor } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import type { FakeRoute } from "../../test/fakeApi";
import { openApiRoutes } from "../../test/openapi";
import { renderPanel } from "../../test/renderPanel";
import ElasticIpsPage from "./ElasticIpsPage";
import LoadBalancersPage from "./LoadBalancersPage";
import NetworksPage from "./NetworksPage";
import SecurityGroupDetailPage from "./SecurityGroupDetailPage";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const projectNetworks = "/proxy/v1/projects/web/networks";
const root = "/apis/bedrock.cloudyfolks.io/v1alpha1";
const ns = `${root}/namespaces/acme-web`;
const asList = (value: unknown) => value as KubeList;

function renderAt(path: string, element: ReactElement, api: Record<string, FakeRoute>, allow: (check: Check) => boolean = () => true, routePath = path) {
  return renderPanel(
    <Routes>
      <Route path={routePath} element={element} />
    </Routes>,
    { route: path, allow, api: { ...openApiRoutes(), "GET /proxy/v1/projects": { body: projects }, ...api } },
  );
}

function generic(plural: string) {
  const def = definitionFor(plural);
  if (!def) {
    throw new Error("missing definition");
  }
  return <ResourceListPage def={def} />;
}

describe("NetworksPage", () => {
  it("lists only the networks of the tenant of the project", async () => {
    const { api } = renderAt("/p/web/networks", <NetworksPage />, { [`GET ${projectNetworks}`]: { body: asList(networks) } }, undefined, "/p/:project/networks");
    const row = (await screen.findByRole("link", { name: "acme" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("10.100.0.0/16")).toBeTruthy();
    expect(screen.queryByRole("link", { name: "beta" })).toBeNull();
    expect(api.calls.some((call) => call.path === `${root}/networks`)).toBe(false);
  });

  it("TestForbiddenNetworkListDegrades", async () => {
    renderAt("/p/web/networks", <NetworksPage />, { [`GET ${projectNetworks}`]: { status: 403, body: { error: "forbidden" } } }, undefined, "/p/:project/networks");
    expect(await screen.findByText("You cannot list networks")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Subnets" }).getAttribute("href")).toBe("/p/web/subnets");
  });

  it("reports when the length of the network list exceeds the limit", async () => {
    renderAt("/p/web/networks", <NetworksPage />, { [`GET ${projectNetworks}`]: { status: 422, body: { error: "network_inventory_limit" } } }, undefined, "/p/:project/networks");
    expect(await screen.findByText("The network list is too large")).toBeTruthy();
  });

  it("presets and locks the tenant on create, and asks for the typed name on delete", async () => {
    const user = userEvent.setup();
    const { api } = renderAt("/p/web/networks", <NetworksPage />, { [`GET ${projectNetworks}`]: { body: asList(networks) }, [`DELETE ${root}/networks/acme`]: { body: {} } }, undefined, "/p/:project/networks");
    await user.click(await screen.findByRole("button", { name: "Create" }));
    const tenant = await screen.findByLabelText(/^tenantRef/, undefined, { timeout: 15000 });
    expect((tenant as HTMLInputElement).value).toBe("acme");
    expect((tenant as HTMLInputElement).readOnly).toBe(true);
    await user.keyboard("{Escape}");
    await user.click(await screen.findByRole("button", { name: "Actions for acme" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("Name"), "acme");
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(api.calls.some((call) => call.method === "DELETE")).toBe(true));
    await waitFor(() => expect(api.calls.filter((call) => call.path === projectNetworks).length).toBeGreaterThan(1));
  });
});

describe("Subnets", () => {
  it("offers the networks as a select, and free text when the list is forbidden", async () => {
    const user = userEvent.setup();
    const view = renderAt("/p/web/subnets", generic("subnets"), { [`GET ${ns}/subnets`]: { body: asList(subnets) }, [`GET ${root}/networks`]: { body: asList(networks) } }, undefined, "/p/:project/subnets");
    await user.click(await screen.findByRole("button", { name: "Create" }));
    expect(await screen.findByRole("combobox", { name: /networkRef/ }, { timeout: 15000 })).toBeTruthy();
    view.unmount();
    renderAt("/p/web/subnets", generic("subnets"), { [`GET ${ns}/subnets`]: { body: asList(subnets) }, [`GET ${root}/networks`]: { status: 403, body: { error: "forbidden" } } }, undefined, "/p/:project/subnets");
    await user.click(await screen.findByRole("button", { name: "Create" }));
    expect((await screen.findByLabelText(/^networkRef/, undefined, { timeout: 15000 })).tagName).toBe("INPUT");
  });
});

describe("ElasticIpsPage", () => {
  const eips = asList(elasticips);
  const detached = { ...eips, items: [{ ...(eips.items[0] as KubeObject), spec: { providerNetwork: "external" } }] };

  it.each([true, false])("checks the VM interface at submit (still compatible: %s)", async (compatible) => {
    const user = userEvent.setup();
    const machines = asList(virtualmachines);
    const vmOptions = { ...machines, items: machines.items.map((item) => item.metadata.name === "web-1" ? {
      ...item, spec: { ...item.spec, networks: [
        { subnetRef: "default", elasticIPRef: "web-ip" },
        { subnetRef: "explicitly-reserved", elasticIPRef: "another-ip" },
        { subnetRef: "already-targeted" },
      ] },
    } : item) };
    const selectedVm = vmOptions.items.find((item) => item.metadata.name === "web-1");
    const claimed = { ...eips.items[0], metadata: { name: "another-ip", namespace: "acme-web" }, spec: { target: { kind: "VirtualMachine", name: "web-1", subnetRef: "already-targeted" } } };
    const attach = renderAt("/p/web/elastic-ips", <ElasticIpsPage />, {
      [`GET ${ns}/elasticips`]: { body: { ...detached, items: [...detached.items, claimed] } },
      [`GET ${ns}/virtualmachines`]: { body: vmOptions },
      [`GET ${ns}/virtualmachines/web-1`]: { body: compatible ? selectedVm : { ...selectedVm, spec: { networks: [{ subnetRef: "other" }] } } },
      [`GET ${ns}/loadbalancers`]: { body: asList(loadbalancers) },
      [`PATCH ${ns}/elasticips/web-ip`]: { body: {} },
    }, undefined, "/p/:project/elastic-ips");
    await user.click(await screen.findByRole("button", { name: "Actions for web-ip" }));
    await user.click(await screen.findByRole("menuitem", { name: "Attach" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("combobox", { name: /^Target$/ }));
    await user.click(await screen.findByRole("option", { name: "web-1" }));
    await user.click(within(dialog).getByRole("combobox", { name: /Subnet/ }));
    expect((await screen.findAllByRole("option")).map((option) => option.textContent)).toEqual(["default"]);
    await user.click(await screen.findByRole("option", { name: "default" }));
    await user.click(within(dialog).getByRole("button", { name: "Attach" }));
    if (!compatible) {
      expect(await screen.findByText("The action failed")).toBeTruthy();
      expect(attach.api.calls.some((call) => call.path === `${ns}/virtualmachines/web-1` && call.method === "GET")).toBe(true);
      expect(attach.api.calls.some((call) => call.method === "PATCH")).toBe(false);
      return;
    }
    await waitFor(() => expect(attach.api.calls.some((call) => call.method === "PATCH")).toBe(true));
    expect(attach.api.calls.find((call) => call.method === "PATCH")?.body).toEqual({ metadata: { resourceVersion: "100" }, spec: { target: { kind: "VirtualMachine", name: "web-1", subnetRef: "default" } } });
    attach.unmount();

    const detach = renderAt("/p/web/elastic-ips", <ElasticIpsPage />, { [`GET ${ns}/elasticips`]: { body: eips }, [`PATCH ${ns}/elasticips/web-ip`]: { body: {} } }, undefined, "/p/:project/elastic-ips");
    await user.click(await screen.findByRole("button", { name: "Actions for web-ip" }));
    await user.click(await screen.findByRole("menuitem", { name: "Detach" }));
    await waitFor(() => expect(detach.api.calls.some((call) => call.method === "PATCH")).toBe(true));
    expect(detach.api.calls.find((call) => call.method === "PATCH")?.body).toEqual({ metadata: { resourceVersion: "100" }, spec: { target: null } });
  });

  it.each(["compatible", "no match", "changed", 403, 404] as const)("checks load balancer compatibility at selection and submit (%s)", async (result) => {
    const user = userEvent.setup();
    const lbs = asList(loadbalancers);
    const fixture = lbs.items[0] as KubeObject;
    const item = (name: string, elasticIPRef?: string, namespace = "acme-web") => ({ ...fixture, metadata: { name, namespace }, spec: { ...fixture.spec, elasticIPRef } });
    const { api } = renderAt("/p/web/elastic-ips", <ElasticIpsPage />, {
      [`GET ${ns}/elasticips`]: { body: detached },
      [`GET ${ns}/virtualmachines`]: { body: asList(virtualmachines) },
      [`GET ${ns}/loadbalancers`]: { body: { ...lbs, items: [
        item("internal"),
        item("other-ip", "not-web-ip"),
        item("other-project", "web-ip", "acme-other"),
        ...(result === "no match" ? [] : [item("matching", "web-ip")]),
      ] } },
      [`GET ${ns}/loadbalancers/matching`]: typeof result === "number"
        ? { status: result }
        : { body: item("matching", result === "changed" ? "not-web-ip" : "web-ip") },
      [`PATCH ${ns}/elasticips/web-ip`]: { body: {} },
    }, undefined, "/p/:project/elastic-ips");
    await user.click(await screen.findByRole("button", { name: "Actions for web-ip" }));
    await user.click(await screen.findByRole("menuitem", { name: "Attach" }));
    const dialog = within(await screen.findByRole("dialog"));
    await user.click(dialog.getByRole("combobox", { name: "Target type" }));
    await user.click(await screen.findByRole("option", { name: "Load balancer" }));
    const attach = dialog.getByRole<HTMLButtonElement>("button", { name: "Attach" });
    expect(attach.disabled).toBe(true);
    if (result === "no match") {
      expect(await dialog.findByText("No load balancer in this project references this Elastic IP. Set its Elastic IP field first.")).toBeTruthy();
      expect(dialog.queryByRole("combobox", { name: "Target" })).toBeNull();
      expect(api.calls.some((call) => call.method === "PATCH")).toBe(false);
      return;
    }
    await user.click(await dialog.findByRole("combobox", { name: "Target" }));
    expect((await screen.findAllByRole("option")).map((option) => option.textContent)).toEqual(["matching"]);
    await user.click(screen.getByRole("option", { name: "matching" }));
    expect(attach.disabled).toBe(false);
    expect(api.calls.some((call) => call.path === `${ns}/loadbalancers/matching`)).toBe(false);
    await user.click(attach);
    if (result !== "compatible") {
      expect(await screen.findByText("The action failed")).toBeTruthy();
      expect(api.calls.some((call) => call.method === "GET" && call.path === `${ns}/loadbalancers/matching`)).toBe(true);
      expect(api.calls.some((call) => call.method === "PATCH")).toBe(false);
      expect(attach.disabled).toBe(true);
      return;
    }
    await waitFor(() => expect(api.calls.filter((call) => call.method === "PATCH").map(({ path, body }) => ({ path, body }))).toEqual([
      { path: `${ns}/elasticips/web-ip`, body: { metadata: { resourceVersion: "100" }, spec: { target: { kind: "LoadBalancer", name: "matching" } } } },
    ]));
    const get = api.calls.findIndex((call) => call.method === "GET" && call.path === `${ns}/loadbalancers/matching`);
    expect(get).toBeGreaterThanOrEqual(0);
    expect(get).toBeLessThan(api.calls.findIndex((call) => call.method === "PATCH"));
  });

  it("does not expose Attach without patch permission", async () => {
    const user = userEvent.setup();
    const { api } = renderAt("/p/web/elastic-ips", <ElasticIpsPage />, { [`GET ${ns}/elasticips`]: { body: detached } }, (check) => check.verb !== "patch", "/p/:project/elastic-ips");
    await user.click(await screen.findByRole("button", { name: "Actions for web-ip" }));
    expect(await screen.findByRole("menuitem", { name: "Edit" })).toBeTruthy();
    expect(screen.queryByRole("menuitem", { name: "Attach" })).toBeNull();
    expect(api.calls.some((call) => call.path.endsWith("/loadbalancers"))).toBe(false);
  });

  it("releases with a typed confirmation", async () => {
    const user = userEvent.setup();
    const { api } = renderAt("/p/web/elastic-ips", <ElasticIpsPage />, { [`GET ${ns}/elasticips`]: { body: eips }, [`DELETE ${ns}/elasticips/web-ip`]: { body: {} } }, undefined, "/p/:project/elastic-ips");
    await user.click(await screen.findByRole("button", { name: "Actions for web-ip" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("Name"), "web-ip");
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(api.calls.some((call) => call.method === "DELETE")).toBe(true));
  });
});

describe("LoadBalancers", () => {
  const lbs = asList(loadbalancers);

  it("takes the address from the ElasticIP that targets the load balancer", async () => {
    const eips = asList(elasticips);
    const attached = { ...eips, items: [{ ...(eips.items[0] as KubeObject), spec: { providerNetwork: "external", target: { kind: "LoadBalancer", name: "web-lb" } } }] };
    renderAt("/p/web/load-balancers", <LoadBalancersPage />, { [`GET ${ns}/loadbalancers`]: { body: lbs }, [`GET ${ns}/elasticips`]: { body: attached } }, undefined, "/p/:project/load-balancers");
    const row = (await screen.findByRole("link", { name: "web-lb" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("TCP 443")).toBeTruthy();
    await waitFor(() => expect(within(row).getByText("web-ip 203.0.113.10")).toBeTruthy());
  });

  it("shows the address that the status reports", async () => {
    const withAddress = { ...lbs, items: [{ ...(lbs.items[0] as KubeObject), status: { address: "100.69.0.2" } }] };
    renderAt("/p/web/load-balancers", <LoadBalancersPage />, { [`GET ${ns}/loadbalancers`]: { body: withAddress }, [`GET ${ns}/elasticips`]: { body: asList(elasticips) } }, undefined, "/p/:project/load-balancers");
    const row = (await screen.findByRole("link", { name: "web-lb" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("100.69.0.2")).toBeTruthy();
  });

  it("shows the configured reference without an unrelated Elastic IP address", async () => {
    renderAt("/p/web/load-balancers", <LoadBalancersPage />, { [`GET ${ns}/loadbalancers`]: { body: lbs }, [`GET ${ns}/elasticips`]: { body: asList(elasticips) } }, undefined, "/p/:project/load-balancers");
    const row = (await screen.findByRole("link", { name: "web-lb" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("web-ip")).toBeTruthy();
    expect(within(row).queryByText(/203\.0\.113\.10/)).toBeNull();
  });

  it("offers only compatible project Elastic IPs on create", async () => {
    const user = userEvent.setup();
    const eips = asList(elasticips);
    const fixture = eips.items[0] as KubeObject;
    const item = (name: string, target?: object, namespace = "acme-web") => ({ ...fixture, metadata: { name, namespace }, spec: { target } });
    renderAt("/p/web/load-balancers", <LoadBalancersPage />, {
      [`GET ${ns}/loadbalancers`]: { body: lbs },
      [`GET ${ns}/elasticips`]: { body: { ...eips, items: [
        item("reserved"),
        item("back-reference", { kind: "LoadBalancer", name: "new-lb" }),
        item("other-lb", { kind: "LoadBalancer", name: "other" }),
        item("vm-ip", { kind: "VirtualMachine", name: "new-lb" }),
        item("other-project", undefined, "acme-other"),
      ] } },
    }, undefined, "/p/:project/load-balancers");
    await user.click(await screen.findByRole("button", { name: "Create" }));
    const dialog = within(await screen.findByRole("dialog"));
    await user.type(await dialog.findByLabelText("Name"), "new-lb");
    const frontend = await dialog.findByRole("combobox", { name: "Elastic IP" }, { timeout: 15000 });
    expect(frontend.textContent).toContain("Internal only");
    await user.click(frontend);
    expect((await screen.findAllByRole("option")).map((option) => option.textContent)).toEqual(["Internal only", "reserved", "back-reference"]);
  });

  it("keeps a saved reference when listing Elastic IPs is forbidden", async () => {
    const user = userEvent.setup();
    const { api } = renderAt("/p/web/load-balancers", <LoadBalancersPage />, {
      [`GET ${ns}/loadbalancers`]: { body: lbs },
      [`GET ${ns}/loadbalancers/web-lb`]: { body: lbs.items[0] },
      [`GET ${ns}/elasticips`]: { status: 403, body: { error: "forbidden" } },
      [`PUT ${ns}/loadbalancers/web-lb`]: { body: lbs.items[0] },
    }, undefined, "/p/:project/load-balancers");
    await user.click(await screen.findByRole("button", { name: "Actions for web-lb" }));
    await user.click(await screen.findByRole("menuitem", { name: "Edit" }));
    const dialog = within(await screen.findByRole("dialog"));
    const frontend = await dialog.findByRole("combobox", { name: "Elastic IP" }, { timeout: 15000 });
    expect(frontend.textContent).toContain("web-ip");
    await user.click(dialog.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.find((call) => call.method === "PUT")?.body).toMatchObject({ spec: { elasticIPRef: "web-ip" } }));
    expect(api.calls.some((call) => call.method === "PATCH")).toBe(false);
  });

  it("saves the chosen frontend without modifying its Elastic IP", async () => {
    const user = userEvent.setup();
    const fixture = lbs.items[0] as KubeObject;
    const internal = { ...fixture, spec: { ...fixture.spec, elasticIPRef: undefined } };
    const eips = asList(elasticips);
    const reserved = { ...eips.items[0], spec: {} };
    const { api } = renderAt("/p/web/load-balancers", <LoadBalancersPage />, {
      [`GET ${ns}/loadbalancers`]: { body: { ...lbs, items: [internal] } },
      [`GET ${ns}/loadbalancers/web-lb`]: { body: internal },
      [`GET ${ns}/elasticips`]: { body: { ...eips, items: [reserved] } },
      [`PUT ${ns}/loadbalancers/web-lb`]: { body: fixture },
    }, undefined, "/p/:project/load-balancers");
    await user.click(await screen.findByRole("button", { name: "Actions for web-lb" }));
    await user.click(await screen.findByRole("menuitem", { name: "Edit" }));
    const dialog = within(await screen.findByRole("dialog"));
    await user.click(await dialog.findByRole("combobox", { name: "Elastic IP" }, { timeout: 15000 }));
    await user.click(await screen.findByRole("option", { name: "web-ip" }));
    await user.click(dialog.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.find((call) => call.method === "PUT")).toMatchObject({
      path: `${ns}/loadbalancers/web-lb`, body: { spec: { elasticIPRef: "web-ip" } },
    }));
    expect(api.calls.filter((call) => call.path.startsWith(`${ns}/elasticips`) && call.method !== "GET")).toHaveLength(0);
  });

  it("TestPortRangeIsValidated", async () => {
    const user = userEvent.setup();
    renderAt("/p/web/load-balancers", generic("loadbalancers"), { [`GET ${ns}/loadbalancers`]: { body: lbs }, [`GET ${ns}/loadbalancers/web-lb`]: { body: lbs.items[0] }, [`GET ${ns}/virtualmachines`]: { body: asList(virtualmachines) } }, undefined, "/p/:project/load-balancers");
    await user.click(await screen.findByRole("button", { name: "Actions for web-lb" }));
    await user.click(await screen.findByRole("menuitem", { name: "Edit" }));
    const port = await screen.findAllByRole("textbox", { name: /^port/ }, { timeout: 15000 });
    await user.clear(port[0] as HTMLElement);
    await user.type(port[0] as HTMLElement, "70000");
    await user.tab();
    expect((port[0] as HTMLInputElement).value).toMatch(/^65.?535$/);
    await user.clear(port[0] as HTMLElement);
    await user.type(port[0] as HTMLElement, "0");
    await user.tab();
    expect((port[0] as HTMLInputElement).value).toBe("1");
  });
});

describe("InternetGateways", () => {
  const gateways = asList(internetgateways);

  it("lists the Network and the Elastic IP of a gateway", async () => {
    renderAt("/p/web/internet-gateways", generic("internetgateways"), { [`GET ${ns}/internetgateways`]: { body: gateways } }, undefined, "/p/:project/internet-gateways");
    const row = (await screen.findByRole("link", { name: "egress" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("acme")).toBeTruthy();
    expect(within(row).getByText("web-ip")).toBeTruthy();
  });

  it("offers the Networks and the Elastic IPs as selects", async () => {
    const user = userEvent.setup();
    renderAt(
      "/p/web/internet-gateways",
      generic("internetgateways"),
      { [`GET ${ns}/internetgateways`]: { body: gateways }, [`GET ${root}/networks`]: { body: asList(networks) }, [`GET ${ns}/elasticips`]: { body: asList(elasticips) }, [`GET ${ns}/subnets`]: { body: asList(subnets) } },
      undefined,
      "/p/:project/internet-gateways",
    );
    await user.click(await screen.findByRole("button", { name: "Create" }));
    expect(await screen.findByRole("combobox", { name: /networkRef/ }, { timeout: 15000 })).toBeTruthy();
    expect(await screen.findByRole("combobox", { name: /elasticIPRef/ }, { timeout: 15000 })).toBeTruthy();
  });
});

describe("DnsZones", () => {
  const zones = asList(dnszones);

  it("shows record names and addresses left to right", async () => {
    renderAt("/p/web/dns-zones", generic("dnszones"), { [`GET ${ns}/dnszones`]: { body: zones } }, undefined, "/p/:project/dns-zones");
    const row = (await screen.findByRole("link", { name: "internal" })).closest("tr") as HTMLElement;
    const bdis = [...row.querySelectorAll('bdi[dir="ltr"]')].map((element) => element.textContent?.trim());
    expect(bdis).toContain("app: 10.100.0.5");
    expect(bdis).toContain("v6: fd00::5");
  });

  it("refuses an invalid address", async () => {
    const user = userEvent.setup();
    const bad = { ...zones, items: [{ ...(zones.items[0] as KubeObject), spec: { zone: "web.internal", records: [{ name: "app", ips: ["999.1.1.1"] }] } }] };
    const { api } = renderAt("/p/web/dns-zones", generic("dnszones"), { [`GET ${ns}/dnszones`]: { body: bad }, [`GET ${ns}/dnszones/internal`]: { body: bad.items[0] } }, undefined, "/p/:project/dns-zones");
    await user.click(await screen.findByRole("button", { name: "Actions for internal" }));
    await user.click(await screen.findByRole("menuitem", { name: "Edit" }));
    await screen.findByLabelText(/^name/, undefined, { timeout: 15000 });
    await user.click(document.querySelector("form button[type=submit]") as HTMLElement);
    expect(await screen.findByText("Enter a valid IPv4 or IPv6 address")).toBeTruthy();
    expect(api.calls.some((call) => call.method === "PUT")).toBe(false);
  });
});

describe("SecurityGroups", () => {
  const groups = asList(securitygroups);
  const group = groups.items[0] as KubeObject;

  it("labels sources and destinations, keeping the warning only on ingress", async () => {
    renderAt("/p/web/security-groups/web", <SecurityGroupDetailPage />, { [`GET ${ns}/securitygroups/web`]: { body: { ...group, spec: { ingress: [...(group.spec?.ingress as unknown[]), { protocol: "tcp", port: 22, cidr: "0.0.0.0/0" }], egress: group.spec?.egress } } } }, undefined, "/p/:project/security-groups/:name");
    const ingress = within(await screen.findByRole("table", { name: "Ingress rules" }));
    const egress = within(screen.getByRole("table", { name: "Egress rules" }));
    expect(ingress.getByRole("columnheader", { name: "Source" })).toBeTruthy();
    expect(egress.getByRole("columnheader", { name: "Destination" })).toBeTruthy();
    expect(ingress.getByRole("cell", { name: "10.0.0.0/8" })).toBeTruthy();
    expect(egress.getByRole("cell", { name: "0.0.0.0/0" })).toBeTruthy();
    expect(screen.queryByRole("columnheader", { name: "Summary" })).toBeNull();
    expect(screen.getAllByText("Open to the internet")).toHaveLength(1);
  });

  it("refuses a rule with both a CIDR and a security group", async () => {
    const user = userEvent.setup();
    const both = { ...group, spec: { ingress: [{ protocol: "tcp", port: 22, cidr: "10.0.0.0/8", securityGroupRef: "other" }], egress: [] } };
    const { api } = renderAt("/p/web/security-groups/web", <SecurityGroupDetailPage />, { [`GET ${ns}/securitygroups/web`]: { body: both } }, undefined, "/p/:project/security-groups/:name");
    await user.click(await screen.findByRole("button", { name: "Edit rules" }));
    await screen.findAllByRole("button", { name: "Add item" }, { timeout: 15000 });
    await user.click(document.querySelector("form button[type=submit]") as HTMLElement);
    expect(await screen.findByText("Set a CIDR or a security group, not both")).toBeTruthy();
    expect(api.calls.some((call) => call.method === "PUT")).toBe(false);
  });
});
