import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { Check, KubeList } from "../../api/types";
import settings from "../../fixtures/settings.json";
import { renderPanel } from "../../test/renderPanel";
import SettingsPage from "./SettingsPage";
import { groupSettings } from "./settingsCatalog";

const list = settings as unknown as KubeList;
const path = "/apis/bedrock.cloudyfolks.io/v1alpha1/settings";

function render(allow: (check: Check) => boolean = () => true, patchReply: { status?: number; body?: unknown } = { body: {} }) {
  return renderPanel(
    <Routes>
      <Route path="/admin/settings" element={<SettingsPage />} />
    </Routes>,
    {
      route: "/admin/settings",
      allow,
      api: { [`GET ${path}`]: { body: list }, [`PATCH ${path}/*`]: patchReply },
    },
  );
}

describe("SettingsPage", () => {
  it("TestSettingsAreGroupedByPrefix", () => {
    const names = ["authn.session-ttl", "kata.enabled", "platform.host", "network.eip-mode", "zzz.custom", "platform.tls-mode"].map((name) => ({ metadata: { name } }));
    expect(groupSettings(names).map((group) => group.prefix)).toEqual(["platform", "network", "kata", "authn", "zzz"]);
    expect(groupSettings(names)[0]?.items.map((item) => item.metadata.name)).toEqual(["platform.host", "platform.tls-mode"]);
  });

  it("shows key, description, default and the applied status", async () => {
    render();
    const row = (await screen.findByText("authn.session-ttl", { selector: "bdi" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("Login session cookie lifetime.")).toBeTruthy();
    expect(within(row).getAllByText("12h").length).toBeGreaterThan(0);
    expect(within(row).getByText("Applied")).toBeTruthy();
  });

  it("TestEditSettingPatchesTheValue", async () => {
    const user = userEvent.setup();
    const { api } = render();
    const row = (await screen.findByText("authn.session-ttl", { selector: "bdi" })).closest("tr") as HTMLElement;
    const input = within(row).getByRole("textbox");
    await user.clear(input);
    await user.type(input, "8h");
    await user.click(within(row).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((call) => call.method === "PATCH")).toBe(true));
    const patch = api.calls.find((call) => call.method === "PATCH");
    expect(patch?.body).toEqual({ spec: { value: "8h" } });
    expect(patch?.path).toBe(`${path}/authn.session-ttl`);
  });

  it("shows a server message under the input", async () => {
    const user = userEvent.setup();
    render(() => true, { status: 422, body: { kind: "Status", reason: "Invalid", message: "value below minimum 5m0s" } });
    const row = (await screen.findByText("authn.session-ttl", { selector: "bdi" })).closest("tr") as HTMLElement;
    const input = within(row).getByRole("textbox");
    await user.clear(input);
    await user.type(input, "1s");
    await user.click(within(row).getByRole("button", { name: "Save" }));
    expect(await within(row).findByText("value below minimum 5m0s")).toBeTruthy();
  });

  it("says when a setting uses the default", async () => {
    render();
    const row = (await screen.findByText("authn.session-ttl", { selector: "bdi" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("Uses the default")).toBeTruthy();
  });

  it("TestPlatformHostWarning", async () => {
    const user = userEvent.setup();
    const { api } = render();
    const row = (await screen.findByText("platform.host", { selector: "bdi" })).closest("tr") as HTMLElement;
    const input = within(row).getByRole("textbox");
    await user.clear(input);
    await user.type(input, "new.example.test");
    await user.click(within(row).getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/sign in again/)).toBeTruthy();
    expect(api.calls.some((call) => call.method === "PATCH")).toBe(false);
    await user.click(screen.getAllByRole("button", { name: "Save" }).at(-1) as HTMLElement);
    await waitFor(() => expect(api.calls.some((call) => call.method === "PATCH")).toBe(true));
  });

  it("TestMultilineSettings uses a left to right monospace textarea", async () => {
    render();
    const row = (await screen.findByText("platform.additional-ca", { selector: "bdi" })).closest("tr") as HTMLElement;
    const area = within(row).getByRole("textbox");
    expect(area.tagName).toBe("TEXTAREA");
    expect(area.getAttribute("dir")).toBe("ltr");
  });

  it("uses a switch for booleans and a select for enums", async () => {
    render();
    const kata = (await screen.findByText("kata.enabled", { selector: "bdi" })).closest("tr") as HTMLElement;
    expect(within(kata).getByRole("switch")).toBeTruthy();
    const mode = screen.getByText("platform.tls-mode", { selector: "bdi" }).closest("tr") as HTMLElement;
    expect(within(mode).getByRole("combobox")).toBeTruthy();
  });

  it("TestReadOnlyWithoutPermission", async () => {
    render((check) => check.verb !== "update");
    const row = (await screen.findByText("authn.session-ttl", { selector: "bdi" })).closest("tr") as HTMLElement;
    await waitFor(() => expect((within(row).getByRole("textbox") as HTMLInputElement).readOnly).toBe(true));
    expect(within(row).queryByRole("button", { name: "Save" })).toBeNull();
  });
});
