import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useLocation } from "react-router";
import { AppRoutes } from "./routes";
import { axeViolations } from "../test/axe";
import { renderPanel, type RenderOptions } from "../test/renderPanel";
import type { Locale } from "../i18n";

const projects = {
  projects: [
    { name: "db", displayName: "Database", tenant: "acme", namespace: "acme-db" },
    { name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" },
  ],
};

function Path() {
  return <span data-testid="path">{useLocation().pathname}</span>;
}

function app(route: string, options: RenderOptions = {}) {
  return renderPanel(
    <>
      <AppRoutes />
      <Path />
    </>,
    { route, ...options, api: { "GET /proxy/v1/projects": { body: projects }, ...options.api } },
  );
}

function hrefs(root: HTMLElement): string[] {
  return within(root)
    .getAllByRole("link")
    .map((link) => link.getAttribute("href") ?? "");
}

describe("shell", () => {
  afterEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
  });

  it("TestAdminSeesBothPerspectives", async () => {
    app("/admin/hosts");
    const perspective = await screen.findByRole("navigation", { name: "Perspective" });
    expect(within(perspective).getByRole("link", { name: "Admin" })).toBeTruthy();
    expect(within(perspective).getByRole("link", { name: "Projects" })).toBeTruthy();
    const nav = screen.getByRole("navigation", { name: "Main navigation" });
    for (const heading of ["Infrastructure", "Platform", "Tenancy", "Catalog"]) {
      expect(within(nav).getByRole("heading", { name: heading })).toBeTruthy();
    }
    expect(hrefs(nav)).toEqual(["/admin/hosts", "/admin/upgrades", "/admin/settings", "/admin/addons", "/admin/tenants", "/admin/cluster-images"]);
  });

  it("TestNonAdminNeverSeesAdmin", async () => {
    app("/admin/hosts", { session: { admin: false, user: { name: "dev", displayName: "Dev", email: "d@x", groups: [] } } });
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/p/db/vms"));
    expect(screen.queryByRole("navigation", { name: "Perspective" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Admin" })).toBeNull();
  });

  it("TestProjectNavFollowsTheRouteTable", async () => {
    app("/p/web/vms");
    const nav = await screen.findByRole("navigation", { name: "Main navigation" });
    expect(hrefs(nav)).toEqual(
      [
        "vms",
        "containers",
        "kubernetes-clusters",
        "ssh-keys",
        "volumes",
        "snapshots",
        "images",
        "backups",
        "networks",
        "subnets",
        "elastic-ips",
        "load-balancers",
        "dns-zones",
        "security-groups",
        "buckets",
        "object-users",
      ].map((path) => `/p/web/${path}`),
    );
    for (const heading of ["Compute", "Storage", "Networking", "Object storage"]) {
      expect(within(nav).getByRole("heading", { name: heading })).toBeTruthy();
    }
  });

  it("TestProjectPickerListsTheProjectsOfTheEndpointAndStoresTheChoice", async () => {
    const user = userEvent.setup();
    app("/p/web/vms");
    await user.click(await screen.findByRole("combobox", { name: /Project/ }));
    expect(await screen.findByRole("option", { name: "Database" })).toBeTruthy();
    await user.click(screen.getByRole("option", { name: "Database" }));
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/p/db/vms"));
    expect(localStorage.getItem("bedrock.panel.project")).toBe("db");
  });

  it("redirects /p to the stored project, else the first, else shows the empty state", async () => {
    localStorage.setItem("bedrock.panel.project", "web");
    const first = app("/p");
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/p/web/vms"));
    first.unmount();
    localStorage.setItem("bedrock.panel.project", "gone");
    const second = app("/p");
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/p/db/vms"));
    second.unmount();
    localStorage.clear();
    app("/p", { api: { "GET /proxy/v1/projects": { body: { projects: [] } } } });
    expect(await screen.findByText("No projects")).toBeTruthy();
  });

  it("TestDeepLinkSelectsTheProject", async () => {
    app("/p/web/volumes");
    const picker = await screen.findByRole("combobox", { name: /Project/ });
    await waitFor(() => expect(picker.textContent).toContain("Web"));
  });

  it("TestUnknownProjectShowsNotFound", async () => {
    const { api } = app("/p/nope/vms");
    expect(await screen.findByText("Page not found")).toBeTruthy();
    expect(api.calls.some((call) => call.path.includes("/namespaces/"))).toBe(false);
  });

  it("TestSignOutPostsWithCsrfAndGoesHome", async () => {
    const user = userEvent.setup();
    const assign = vi.fn();
    const { api } = app("/p/web/vms", { assign, api: { "POST /oauth/logout": { status: 204 } } });
    await user.click(await screen.findByRole("button", { name: "User menu" }));
    await user.click(await screen.findByRole("menuitem", { name: "Sign out" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/"));
    const call = api.calls.find((entry) => entry.path === "/oauth/logout");
    expect(call?.method).toBe("POST");
    expect(call?.headers.get("X-CSRF-Token")).toBeTruthy();
  });

  it("TestDownloadLinks", async () => {
    const user = userEvent.setup();
    app("/p/web/vms", { api: { "HEAD /ca.crt": { status: 200 } } });
    await user.click(await screen.findByRole("button", { name: "User menu" }));
    const kubeconfig = await screen.findByRole("menuitem", { name: "Download kubeconfig" });
    expect(kubeconfig.getAttribute("href")).toBe("/kubeconfig");
    expect(kubeconfig.hasAttribute("download")).toBe(true);
    const ca = await screen.findByRole("menuitem", { name: "Download CA certificate" });
    expect(ca.getAttribute("href")).toBe("/ca.crt");
    expect(ca.hasAttribute("download")).toBe(true);
  });

  it("hides the CA link when none exists", async () => {
    const user = userEvent.setup();
    const { api } = app("/p/web/vms", { api: { "HEAD /ca.crt": { status: 404, body: { error: "not_found" } } } });
    await user.click(await screen.findByRole("button", { name: "User menu" }));
    await screen.findByRole("menuitem", { name: "Download kubeconfig" });
    await waitFor(() => expect(api.calls.some((call) => call.method === "HEAD")).toBe(true));
    expect(screen.queryByRole("menuitem", { name: "Download CA certificate" })).toBeNull();
  });

  it("TestLanguageAndThemeChoices", async () => {
    const user = userEvent.setup();
    app("/p/web/vms");
    await user.click(await screen.findByRole("combobox", { name: /Language/ }));
    await user.click(await screen.findByRole("option", { name: "فارسی" }));
    expect(document.documentElement.lang).toBe("fa");
    expect(document.documentElement.dir).toBe("rtl");
    expect(localStorage.getItem("bedrock.panel.locale")).toBe("fa");
    await user.click(screen.getAllByRole("combobox")[2] as HTMLElement);
    await user.click(await screen.findByRole("option", { name: /تیره|Dark/ }));
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(localStorage.getItem("bedrock.panel.theme")).toBe("dark");
  });

  it("TestSkipLink", async () => {
    const user = userEvent.setup();
    app("/p/web/vms");
    await screen.findByRole("navigation", { name: "Main navigation" });
    await user.tab();
    const link = document.activeElement as HTMLAnchorElement;
    expect(link.textContent).toBe("Skip to content");
    expect(link.getAttribute("href")).toBe("#content");
    expect(document.getElementById("content")).toBeTruthy();
  });

  it.each(["en", "fa", "ar"] as Locale[])("TestA11yBasics in %s", async (locale) => {
    const { container } = app("/p/web/vms", { locale });
    await screen.findByRole("navigation", { name: /./, hidden: false }).catch(() => undefined);
    await waitFor(() => expect(container.querySelector(".br-nav-link")).toBeTruthy());
    expect(await axeViolations(container)).toEqual([]);
  });
});
