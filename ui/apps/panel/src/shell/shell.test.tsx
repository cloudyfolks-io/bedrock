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

async function openGroup(user: ReturnType<typeof userEvent.setup>, nav: HTMLElement, name: string) {
  const group = await within(nav).findByRole("button", { name });
  if (group.getAttribute("aria-expanded") === "false") {
    await user.click(group);
  }
}

describe("shell", () => {
  afterEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
  });

  it("shows administration and project resources in one navigation", async () => {
    const user = userEvent.setup();
    app("/admin/hosts");
    const picker = await screen.findByRole("combobox", { name: "Project" });
    expect(picker.textContent).toContain("Database");
    expect(screen.queryByRole("navigation", { name: "Perspective" })).toBeNull();
    const nav = screen.getByRole("navigation", { name: "Main navigation" });
    const administration = within(nav).getByRole("group", { name: "Administration" });
    const project = within(nav).getByRole("group", { name: "Project resources" });
    expect(within(administration).getByRole("button", { name: "Infrastructure" }).getAttribute("aria-expanded")).toBe("true");
    for (const name of ["Platform", "Tenancy", "Catalog", "Compute", "Storage", "Networking", "Databases", "Object storage", "Registry"]) {
      expect(within(nav).getByRole("button", { name }).getAttribute("aria-expanded")).toBe("false");
    }
    for (const group of within(nav).getAllByRole("button")) {
      expect(group.querySelector('.scloud-nav-group-icon[aria-hidden="true"] path')?.getAttribute("d")).toBeTruthy();
    }
    expect(within(nav).getByRole("link", { name: "Hosts" }).getAttribute("aria-current")).toBe("page");
    expect(within(project).queryByRole("link", { name: "Virtual machines" })).toBeNull();
    await openGroup(user, project, "Compute");
    expect(within(project).getByRole("link", { name: "Virtual machines" }).getAttribute("href")).toBe("/p/db/vms");

    for (const name of ["Platform", "Tenancy", "Catalog"]) {
      await openGroup(user, nav, name);
    }
    expect(hrefs(nav).filter((href) => href.startsWith("/admin/"))).toEqual(["/admin/overview", "/admin/inventory", "/admin/hosts", "/admin/upgrades", "/admin/network-peerings", "/admin/settings", "/admin/addons", "/admin/tenants", "/admin/cluster-images", "/admin/database-versions", "/admin/database-classes"]);
  });

  it("toggles a group using the keyboard and removes collapsed links from navigation", async () => {
    const user = userEvent.setup();
    app("/admin/hosts");
    const nav = screen.getByRole("navigation", { name: "Main navigation" });
    const platform = within(nav).getByRole("button", { name: "Platform" });
    const list = document.getElementById(platform.getAttribute("aria-controls") ?? "");
    expect(list?.hidden).toBe(true);
    expect(within(nav).queryByRole("link", { name: "Addons" })).toBeNull();
    platform.focus();
    await user.keyboard("{Enter}");
    expect(platform.getAttribute("aria-expanded")).toBe("true");
    expect(list?.hidden).toBe(false);
    await user.tab();
    expect(document.activeElement).toBe(within(nav).getByRole("link", { name: "Settings" }));
    await user.tab({ shift: true });
    await user.keyboard(" ");
    expect(platform.getAttribute("aria-expanded")).toBe("false");
    expect(list?.hidden).toBe(true);
  });

  it("TestNonAdminNeverSeesAdmin", async () => {
    app("/admin/hosts", { session: { admin: false, user: { name: "dev", displayName: "Dev", email: "d@x", groups: [] } } });
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/p/db/vms"));
    const nav = screen.getByRole("navigation", { name: "Main navigation" });
    expect(within(nav).queryByRole("group", { name: "Administration" })).toBeNull();
    for (const name of ["Infrastructure", "Platform", "Tenancy", "Catalog"]) {
      expect(within(nav).queryByRole("button", { name })).toBeNull();
    }
    expect(hrefs(nav).every((href) => href.startsWith("/p/db/"))).toBe(true);
  });

  it("opens the overview for an administrator on first sign-in and uses their saved preference later", async () => {
    const first = app("/");
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/admin/overview"));
    first.unmount();
    localStorage.setItem("bedrock.panel.perspective", "project");
    app("/");
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/p/db/vms"));
  });

  it("TestProjectNavFollowsTheRouteTable", async () => {
    const user = userEvent.setup();
    app("/p/web/vms");
    const nav = await screen.findByRole("navigation", { name: "Main navigation" });
    for (const name of ["Compute", "Storage", "Networking", "Databases", "Object storage", "Registry"]) {
      await openGroup(user, nav, name);
    }
    expect(hrefs(nav).filter((href) => href.startsWith("/p/"))).toEqual(
      [
        "vms",
        "containers",
        "builds",
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
        "internet-gateways",
        "dns-zones",
        "security-groups",
        "databases",
        "database-users",
        "database-schemas",
        "buckets",
        "object-users",
        "registries",
        "registry-credentials",
      ].map((path) => `/p/web/${path}`),
    );
  });

  it("scopes project links from administration with the keyboard without leaving the page", async () => {
    const user = userEvent.setup();
    app("/admin/hosts");
    const picker = await screen.findByRole("combobox", { name: "Project" });
    picker.focus();
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("option", { name: "Web" })).toBeTruthy();
    await user.keyboard("{End}{Enter}");
    await waitFor(() => expect(picker.textContent).toContain("Web"));
    expect(screen.getByTestId("path").textContent).toBe("/admin/hosts");
    expect(localStorage.getItem("bedrock.panel.project")).toBe("web");

    const nav = screen.getByRole("navigation", { name: "Main navigation" });
    await openGroup(user, nav, "Storage");
    const volumes = within(nav).getByRole("link", { name: "Volumes" });
    expect(volumes.getAttribute("href")).toBe("/p/web/volumes");
    await user.click(volumes);
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/p/web/volumes"));
    await openGroup(user, nav, "Infrastructure");
    await user.click(within(nav).getByRole("link", { name: "Hosts" }));
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/admin/hosts"));
    expect(picker.textContent).toContain("Web");
  });

  it("keeps the resource list when changing project from a detail page", async () => {
    const user = userEvent.setup();
    app("/p/web/volumes/some-volume");
    await user.click(await screen.findByRole("combobox", { name: "Project" }));
    await user.click(await screen.findByRole("option", { name: "Database" }));
    await waitFor(() => expect(screen.getByTestId("path").textContent).toBe("/p/db/volumes"));
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

  it("expands the current resource group and selects the project on a deep link", async () => {
    app("/p/web/volumes/some-volume");
    const picker = await screen.findByRole("combobox", { name: /Project/ });
    expect(picker.textContent).toContain("Web");
    const nav = screen.getByRole("navigation", { name: "Main navigation" });
    expect(within(nav).getByRole("button", { name: "Storage" }).getAttribute("aria-expanded")).toBe("true");
    expect(within(nav).getByRole("button", { name: "Compute" }).getAttribute("aria-expanded")).toBe("false");
    expect(within(nav).getByRole("link", { name: "Volumes" }).getAttribute("aria-current")).toBe("page");
    expect(within(nav).getByRole("button", { name: "Networking" }).getAttribute("aria-expanded")).toBe("false");
  });

  it("TestUnknownProjectShowsNotFound", async () => {
    const { api } = app("/p/nope/vms");
    expect(await screen.findByText("Page not found")).toBeTruthy();
    expect(api.calls.some((call) => call.path.includes("/namespaces/"))).toBe(false);
  });

  it("shows retry when the project list fails and sends no namespace request", async () => {
    const { api } = app("/p/web/vms", { api: { "GET /proxy/v1/projects": { status: 503 } } });
    expect(await screen.findByRole("button", { name: "Retry" })).toBeTruthy();
    expect(screen.queryByText("Page not found")).toBeNull();
    expect(api.calls.some((call) => call.path.includes("/namespaces/"))).toBe(false);
  });

  it("waits for a project namespace before opening resource pages", async () => {
    const pending = { projects: [{ name: "pending", displayName: "Pending", tenant: "acme", namespace: "" }] };
    const { api } = app("/p/pending/vms", { api: { "GET /proxy/v1/projects": { body: pending } } });
    expect(await screen.findByText("The project namespace is not ready")).toBeTruthy();
    expect(api.calls.some((call) => call.path.endsWith("/virtualmachines"))).toBe(false);
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

  it("keeps full dark mode when changing language", async () => {
    const user = userEvent.setup();
    app("/p/web/vms");
    await user.click(await screen.findByRole("combobox", { name: /Language/ }));
    await user.click(await screen.findByRole("option", { name: "فارسی" }));
    expect(document.documentElement.lang).toBe("fa");
    expect(document.documentElement.dir).toBe("rtl");
    expect(localStorage.getItem("bedrock.panel.locale")).toBe("fa");
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(localStorage.getItem("bedrock.panel.theme")).toBeNull();
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

  it("closes the navigation disclosure on Escape and restores focus", async () => {
    const user = userEvent.setup();
    app("/p/web/vms");
    const nav = await screen.findByRole("navigation", { name: "Main navigation" });
    const toggle = screen.getByRole("button", { name: "Main navigation" });
    await user.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    (await within(nav).findByRole("link", { name: "Virtual machines" })).focus();
    await user.keyboard("{Escape}");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(document.activeElement).toBe(toggle);
  });

  it("closes the navigation disclosure and focuses content after activating a link", async () => {
    const user = userEvent.setup();
    app("/p/web/vms");
    const toggle = screen.getByRole("button", { name: "Main navigation" });
    await user.click(toggle);
    const nav = screen.getByRole("navigation", { name: "Main navigation" });
    await openGroup(user, nav, "Storage");
    await user.click(await within(nav).findByRole("link", { name: "Volumes" }));
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("main")));
  });

  it.each(["en", "fa", "ar"] as Locale[])("TestA11yBasics in %s", async (locale) => {
    const { container } = app("/p/web/vms", { locale });
    await screen.findByRole("navigation", { name: /./, hidden: false }).catch(() => undefined);
    await waitFor(() => expect(container.querySelector(".br-nav-link")).toBeTruthy());
    expect(await axeViolations(container)).toEqual([]);
  });
});
