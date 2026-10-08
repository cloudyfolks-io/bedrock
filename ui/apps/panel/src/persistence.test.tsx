import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { AppRoutes } from "./shell/routes";
import { renderPanel } from "./test/renderPanel";

const documented = ["bedrock.panel.locale", "bedrock.panel.theme", "bedrock.panel.perspective", "bedrock.panel.project"];

describe("persistence", () => {
  afterEach(() => localStorage.clear());

  it("TestOnlyDocumentedKeysAreStored", async () => {
    const user = userEvent.setup();
    renderPanel(<AppRoutes />, {
      route: "/admin/hosts",
      api: { "GET /proxy/v1/projects": { body: { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] } } },
    });
    await user.click(await screen.findByRole("link", { name: "Projects" }));
    await waitFor(() => expect(localStorage.getItem("bedrock.panel.project")).toBe("web"));
    await user.click(screen.getByRole("combobox", { name: /Language/ }));
    await user.click(await screen.findByRole("option", { name: "العربية" }));
    await user.click(screen.getAllByRole("combobox")[2] as HTMLElement);
    await user.click((await screen.findAllByRole("option"))[2] as HTMLElement);
    const keys = Object.keys(localStorage);
    expect(keys.length).toBeGreaterThanOrEqual(4);
    for (const key of keys) {
      expect(documented).toContain(key);
    }
  });
});
