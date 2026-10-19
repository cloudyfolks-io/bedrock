import { screen, within } from "@testing-library/react";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { KubeList } from "../../api/types";
import databases from "../../fixtures/databases.json";
import users from "../../fixtures/databaseusers.json";
import { definitionFor } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { openApiRoutes } from "../../test/openapi";
import { renderPanel } from "../../test/renderPanel";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const ns = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web";

function list(plural: string, path: string, body: unknown) {
  const def = definitionFor(plural);
  if (!def) {
    throw new Error("missing definition");
  }
  renderPanel(
    <Routes>
      <Route path={`/p/:project/${path}`} element={<ResourceListPage def={def} />} />
    </Routes>,
    { route: `/p/web/${path}`, api: { ...openApiRoutes(), "GET /proxy/v1/projects": { body: projects }, [`GET ${ns}/${plural}`]: { body: body as KubeList } } },
  );
}

describe("Databases", () => {
  it("lists the phase, the engine, the version, the class and the proxy address", async () => {
    list("databases", "databases", databases);
    const orders = (await screen.findByRole("link", { name: "orders" })).closest("tr") as HTMLElement;
    expect(within(orders).getByText("Ready")).toBeTruthy();
    expect(within(orders).getByText("postgresql")).toBeTruthy();
    expect(within(orders).getByText("17")).toBeTruthy();
    expect(within(orders).getByText("small")).toBeTruthy();
    expect(Array.from(orders.querySelectorAll('bdi[dir="ltr"]')).map((element) => element.textContent?.trim())).toContain("10.100.0.20");
    const reports = (await screen.findByRole("link", { name: "reports" })).closest("tr") as HTMLElement;
    expect(within(reports).getByText("Provisioning")).toBeTruthy();
  });

  it("lists the user name, the number of grants and the status of a database user", async () => {
    list("databaseusers", "database-users", users);
    const alice = (await screen.findByRole("link", { name: "alice" })).closest("tr") as HTMLElement;
    expect(within(alice).getByText("orders")).toBeTruthy();
    expect(within(alice).getByText("2")).toBeTruthy();
    const reporter = (await screen.findByRole("link", { name: "reporter" })).closest("tr") as HTMLElement;
    expect(within(reporter).getByText("report_reader")).toBeTruthy();
  });
});
