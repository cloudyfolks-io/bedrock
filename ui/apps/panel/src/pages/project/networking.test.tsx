import type { ReactElement } from "react";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { Check, KubeList, KubeObject } from "../../api/types";
import dnszones from "../../fixtures/dnszones.json";
import elasticips from "../../fixtures/elasticips.json";
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
    renderAt("/p/web/networks", <NetworksPage />, { [`GET ${root}/networks`]: { body: asList(networks) } }, undefined, "/p/:project/networks");
    const row = (await screen.findByRole("link", { name: "acme" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("10.100.0.0/16")).toBeTruthy();
    expect(screen.queryByRole("link", { name: "beta" })).toBeNull();
  });

  it("TestForbiddenNetworkListDegrades", async () => {
    renderAt("/p/web/networks", <NetworksPage />, { [`GET ${root}/networks`]: { status: 403, body: { error: "forbidden" } } }, undefined, "/p/:project/networks");
    expect(await screen.findByText("You cannot list networks")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Subnets" }).getAttribute("href")).toBe("/p/web/subnets");
  });

  it("presets and locks the tenant on create, and asks for the typed name on delete", async () => {
    const user = userEvent.setup();
    const { api } = renderAt("/p/web/networks", <NetworksPage />, { [`GET ${root}/networks`]: { body: asList(networks) }, [`DELETE ${root}/networks/acme`]: { body: {} } }, undefined, "/p/:project/networks");
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

  it("TestAttachAndDetachPatchTheTarget", async () => {
    const user = userEvent.setup();
    const attach = renderAt("/p/web/elastic-ips", <ElasticIpsPage />, {
      [`GET ${ns}/elasticips`]: { body: detached },
      [`GET ${ns}/virtualmachines`]: { body: asList(virtualmachines) },
      [`GET ${ns}/loadbalancers`]: { body: asList(loadbalancers) },
      [`PATCH ${ns}/elasticips/web-ip`]: { body: {} },
    }, undefined, "/p/:project/elastic-ips");
    await user.click(await screen.findByRole("button", { name: "Actions for web-ip" }));
    await user.click(await screen.findByRole("menuitem", { name: "Attach" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("combobox", { name: /^Target$/ }));
    await user.click(await screen.findByRole("option", { name: "web-1" }));
    await user.click(within(dialog).getByRole("combobox", { name: /Subnet/ }));
    await user.click(await screen.findByRole("option", { name: "default" }));
    await user.click(within(dialog).getByRole("button", { name: "Attach" }));
    await waitFor(() => expect(attach.api.calls.some((call) => call.method === "PATCH")).toBe(true));
    expect(attach.api.calls.find((call) => call.method === "PATCH")?.body).toEqual({ spec: { target: { kind: "VirtualMachine", name: "web-1", subnetRef: "default" } } });
    attach.unmount();

    const detach = renderAt("/p/web/elastic-ips", <ElasticIpsPage />, { [`GET ${ns}/elasticips`]: { body: eips }, [`PATCH ${ns}/elasticips/web-ip`]: { body: {} } }, undefined, "/p/:project/elastic-ips");
    await user.click(await screen.findByRole("button", { name: "Actions for web-ip" }));
    await user.click(await screen.findByRole("menuitem", { name: "Detach" }));
    await waitFor(() => expect(detach.api.calls.some((call) => call.method === "PATCH")).toBe(true));
    expect(detach.api.calls.find((call) => call.method === "PATCH")?.body).toEqual({ spec: { target: null } });
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

  it("shows a dash when no ElasticIP targets it, even if elasticIPRef is set", async () => {
    renderAt("/p/web/load-balancers", <LoadBalancersPage />, { [`GET ${ns}/loadbalancers`]: { body: lbs }, [`GET ${ns}/elasticips`]: { body: asList(elasticips) } }, undefined, "/p/:project/load-balancers");
    const row = (await screen.findByRole("link", { name: "web-lb" })).closest("tr") as HTMLElement;
    expect(within(row).queryByText(/web-ip/)).toBeNull();
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

  it("shows rule tables with summaries and an open SSH warning", async () => {
    renderAt("/p/web/security-groups/web", <SecurityGroupDetailPage />, { [`GET ${ns}/securitygroups/web`]: { body: { ...group, spec: { ingress: [...(group.spec?.ingress as unknown[]), { protocol: "tcp", port: 22, cidr: "0.0.0.0/0" }], egress: group.spec?.egress } } } }, undefined, "/p/:project/security-groups/:name");
    expect(await screen.findByRole("table", { name: "Ingress rules" })).toBeTruthy();
    expect(screen.getByRole("table", { name: "Egress rules" })).toBeTruthy();
    expect(screen.getByText("tcp 22 from 10.0.0.0/8")).toBeTruthy();
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
