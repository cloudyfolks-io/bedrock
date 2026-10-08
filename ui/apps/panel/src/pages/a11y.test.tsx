import { waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { KubeList } from "../api/types";
import { primaryDefinitionsFor, definitions } from "../resources/registry";
import { AppRoutes } from "../shell/routes";
import type { FakeRoute } from "../test/fakeApi";
import { axeViolations } from "../test/axe";
import { renderPanel } from "../test/renderPanel";
import type { Locale } from "../i18n";

const fixtures = import.meta.glob("../fixtures/*.json", { eager: true, import: "default" }) as Record<string, KubeList>;
const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const base = "/apis/bedrock.cloudyfolks.io/v1alpha1";

function apiRoutes(): Record<string, FakeRoute> {
  const routes: Record<string, FakeRoute> = { "GET /proxy/v1/projects": { body: projects } };
  for (const def of definitions) {
    const list = fixtures[`../fixtures/${def.plural}.json`];
    const path = def.scope === "Namespaced" ? `${base}/namespaces/acme-web/${def.plural}` : `${base}/${def.plural}`;
    routes[`GET ${path}`] = { body: list };
  }
  return routes;
}

const paths = [...primaryDefinitionsFor("admin").map((def) => `/admin/${def.path}`), ...primaryDefinitionsFor("project").map((def) => `/p/web/${def.path}`), "/account"];

describe("every page", () => {
  for (const locale of ["en", "ar"] as Locale[]) {
    for (const path of paths) {
      it(`TestEveryPageHasNoAxeViolations ${path} in ${locale}`, async () => {
        const { container } = renderPanel(<AppRoutes />, { route: path, locale, api: apiRoutes() });
        await waitFor(() => expect(container.querySelector("main h1")).toBeTruthy());
        await waitFor(() => expect(container.querySelector("main .br-spinner")).toBeNull());
        await new Promise((resolve) => setTimeout(resolve, 50));
        expect(await axeViolations(container)).toEqual([]);
      });
    }
  }
});
