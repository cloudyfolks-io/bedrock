import { screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { AppRoutes } from "./routes";
import { renderPanel } from "../test/renderPanel";

describe("shell in right-to-left", () => {
  it("TestShellMirrorsInRtl", async () => {
    const { container } = renderPanel(<AppRoutes />, {
      route: "/p/web/vms",
      locale: "fa",
      api: { "GET /proxy/v1/projects": { body: { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] } } },
    });
    expect(document.documentElement.dir).toBe("rtl");
    const nav = container.querySelector("nav.br-sidebar-links");
    expect(nav?.querySelector('[href="/admin/overview"]')).toBeTruthy();
    await waitFor(() => expect(nav?.querySelector('[href="/p/web/vms"][aria-current="page"]')).toBeTruthy());
    expect(screen.getAllByRole("combobox").some((picker) => picker.textContent?.includes("Web"))).toBe(true);
    await waitFor(() => expect(container.querySelectorAll(".br-icon--directional").length).toBeGreaterThan(0));
    expect(container.querySelector(".br-breadcrumb-separator.br-icon--directional")).toBeTruthy();
    expect(screen.getByRole("link", { name: /ماشین/ })).toBeTruthy();
  });
});
