import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { Check, KubeList, KubeObject } from "../../api/types";
import settings from "../../fixtures/settings.json";
import type { FakeReply, FakeRoute } from "../../test/fakeApi";
import { defaultSession, renderPanel } from "../../test/renderPanel";
import SettingsPage from "./SettingsPage";
import { groupSettings, settingMeta } from "./settingsCatalog";

const list = settings as unknown as KubeList;
const path = "/apis/bedrock.cloudyfolks.io/v1alpha1/settings";

function render({ allow = () => true, patch = { body: {} }, get = { body: list } }: {
  allow?: (check: Check) => boolean;
  patch?: FakeRoute;
  get?: FakeRoute;
} = {}) {
  return renderPanel(
    <Routes>
      <Route path="/admin/settings" element={<SettingsPage />} />
    </Routes>,
    { route: "/admin/settings", allow, api: { [`GET ${path}`]: get, [`PATCH ${path}/*`]: patch } },
  );
}

async function setting(label: string) {
  return within((await screen.findByRole("heading", { level: 3, name: label })).closest("li")!);
}

function chooseCategory(user: ReturnType<typeof userEvent.setup>, name: RegExp) {
  return user.click(within(screen.getByRole("navigation", { name: "Categories" })).getByRole("button", { name }));
}

async function editSetting(user: ReturnType<typeof userEvent.setup>, label: string) {
  const row = await setting(label);
  await user.click(await row.findByRole("button", { name: `Edit ${label}` }));
  return row;
}

function withSessionValue(value: string) {
  return { ...list, items: list.items.map((item) => item.metadata.name === "authn.session-ttl" ? { ...item, spec: { value } } : item) };
}

describe("SettingsPage", () => {
  it("puts the platform hostname and TLS first and preserves the editor kinds", () => {
    const names = ["authn.session-ttl", "kata.enabled", "platform.additional-ca", "platform.host", "network.eip-mode", "zzz.custom", "platform.tls-mode"].map((name) => ({ metadata: { name } }));
    expect(groupSettings(names).map((group) => group.prefix)).toEqual(["platform", "authn", "network", "kata", "zzz"]);
    expect(groupSettings(names)[0]?.items.map((item) => item.metadata.name)).toEqual(["platform.host", "platform.tls-mode", "platform.additional-ca"]);
    expect(settingMeta("backup.archive-recipients").kind).toBe("multiline");
    expect(settingMeta("network.eip-mode").options).toEqual(["l2", "bgp"]);
  });

  it("navigates and searches human text or a raw key across 145 settings", async () => {
    const user = userEvent.setup();
    const extra = Array.from({ length: 140 }, (_, index) => ({ metadata: { name: `scale-${index % 23}.sample-${index}` } }));
    render({ allow: () => false, get: { body: { ...list, items: [...list.items, ...extra] } } });
    await setting("Platform hostname");
    expect(screen.getAllByRole("heading", { level: 3 }).map((heading) => heading.textContent)).toEqual([
      "Platform hostname", "TLS certificate source", "Additional trusted certificates",
    ]);
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
    await user.selectOptions(screen.getByRole("combobox", { name: "Categories" }), "scale-22");
    expect(await screen.findByRole("heading", { name: "Sample 137" })).toBeTruthy();
    expect(screen.getByRole("status").textContent).toBe("Settings shown: 6");
    const search = screen.getByRole("searchbox", { name: "Search all settings" });
    await user.type(search, "login cookie");
    expect(await screen.findByRole("heading", { name: "Session lifetime" })).toBeTruthy();
    await user.clear(search);
    await user.type(search, "scale-0.sample-138");
    expect(await screen.findByRole("heading", { name: "Sample 138" })).toBeTruthy();
    expect(screen.getByRole("status").textContent).toBe("Settings shown: 1");
    await user.type(search, "absent");
    expect(screen.getByRole("heading", { name: "No match" })).toBeTruthy();
    await chooseCategory(user, /^Platform access/);
    expect(await screen.findByRole("heading", { name: "Platform hostname" })).toBeTruthy();
    expect((search as HTMLInputElement).value).toBe("");
  });

  it("discloses the key and default and distinguishes an equal override from inheritance", async () => {
    const user = userEvent.setup();
    render();
    const mode = await setting("TLS certificate source");
    expect(mode.getByText("Override")).toBeTruthy();
    expect(mode.queryByText("Uses the default")).toBeNull();
    expect((mode.getByText("Details").parentElement as HTMLDetailsElement).open).toBe(false);
    await user.click(mode.getByText("Details"));
    expect((mode.getByText("Details").parentElement as HTMLDetailsElement).open).toBe(true);
    expect(mode.getByText("platform.tls-mode")).toBeTruthy();
    expect(mode.getByText("Default")).toBeTruthy();
    await chooseCategory(user, /^Sign-in and sessions/);
    const session = await setting("Session lifetime");
    expect(session.getByText("Uses the default")).toBeTruthy();
    expect(session.getByText("Login session cookie lifetime.")).toBeTruthy();
  });

  it("reports only current controller validation and keeps the rejection reason and message visible", async () => {
    const ready = (status: string, reason: string, message: string, observedGeneration: number) => ({ type: "Ready", status, reason, message, observedGeneration });
    const status = (name: string, observedGeneration: number, conditions: ReturnType<typeof ready>[]): KubeObject => ({
      metadata: { name, generation: 2 },
      status: { default: "", observedGeneration, conditions },
    });
    render({ get: { body: { ...list, items: [
      status("platform.unknown-test", 2, [ready("False", "UnknownKey", "no such setting in the catalog", 2)]),
      status("platform.stale-test", 2, [ready("False", "Invalid", "previous error", 1)]),
      status("platform.valid-test", 2, [ready("True", "Valid", "", 2)]),
      status("platform.waiting-test", 1, []),
      status("platform.unobserved-test", 1, [ready("True", "Valid", "", 0)]),
      status("platform.unknown-state", 2, [ready("Unknown", "Indeterminate", "controller cannot validate yet", 2)]),
    ] } } });
    const rejected = await setting("Unknown test");
    expect(rejected.getByText("Rejected")).toBeTruthy();
    expect(rejected.getByText("UnknownKey")).toBeTruthy();
    expect(rejected.getByText("no such setting in the catalog")).toBeTruthy();
    const stale = await setting("Stale test");
    expect(stale.getByText("Awaiting validation")).toBeTruthy();
    expect(stale.queryByText("previous error")).toBeNull();
    expect((await setting("Valid test")).getByText("Validated")).toBeTruthy();
    expect((await setting("Waiting test")).getByText("Awaiting validation")).toBeTruthy();
    expect((await setting("Unobserved test")).getByText("Awaiting validation")).toBeTruthy();
    expect((await setting("Unknown state")).getByText("Validation unknown")).toBeTruthy();
    expect(screen.queryByText("Applied")).toBeNull();
  });

  it("preserves a draft across navigation and patches the value with CSRF and resource version", async () => {
    const user = userEvent.setup();
    let finish!: (reply: FakeReply) => void;
    const response = new Promise<FakeReply>((resolve) => { finish = resolve; });
    let snapshot = list;
    const { api } = render({ patch: () => response, get: () => ({ body: snapshot }) });
    await setting("Platform hostname");
    await chooseCategory(user, /^Sign-in and sessions/);
    const row = await editSetting(user, "Session lifetime");
    expect(row.queryByRole("button", { name: "Save" })).toBeNull();
    const input = row.getByRole("textbox", { name: "Value for Session lifetime" }) as HTMLInputElement;
    await user.type(input, "8h");
    await chooseCategory(user, /^Platform access/);
    await chooseCategory(user, /^Sign-in and sessions/);
    expect((row.getByRole("textbox") as HTMLInputElement).value).toBe("8h");
    await user.click(row.getByRole("button", { name: "Save" }));
    expect(await row.findByRole("button", { name: "Saving" })).toBeTruthy();
    const patch = api.calls.find((call) => call.method === "PATCH");
    expect(patch?.body).toEqual({ metadata: { resourceVersion: "100" }, spec: { value: "8h" } });
    expect(patch?.headers.get("X-CSRF-Token")).toBe(defaultSession.csrf);
    expect(patch?.path).toBe(`${path}/authn.session-ttl`);
    snapshot = withSessionValue("8h");
    await act(async () => { finish({ body: snapshot.items.find((item) => item.metadata.name === "authn.session-ttl") }); });
    expect((await row.findByRole("status")).textContent).toBe("Change saved");
    expect(row.getByText("Awaiting validation")).toBeTruthy();
    expect(await row.findByText("8h")).toBeTruthy();
  });

  it("keeps server errors and refreshes on conflict without overwriting the draft", async () => {
    const user = userEvent.setup();
    let snapshot = list;
    render({
      patch: () => {
        snapshot = withSessionValue("12h"); // The effective value did not change, but the source did.
        return { status: 409, body: { kind: "Status", reason: "Conflict", message: "the object has been modified" } };
      },
      get: () => ({ body: snapshot }),
    });
    await setting("Platform hostname");
    await chooseCategory(user, /^Sign-in and sessions/);
    const row = await editSetting(user, "Session lifetime");
    const input = row.getByRole("textbox") as HTMLInputElement;
    await user.type(input, "8h");
    await user.click(row.getByRole("button", { name: "Save" }));
    expect(await row.findByText("the object has been modified")).toBeTruthy();
    expect(await row.findByRole("alert")).toBeTruthy();
    expect(input.value).toBe("8h");
    expect(row.queryByRole("button", { name: "Save" })).toBeNull();
    await user.click(row.getByRole("button", { name: "Use current value" }));
    expect(input.value).toBe("12h");
  });

  it("can remove an explicit override, even when it already equals the default", async () => {
    const user = userEvent.setup();
    const { api } = render();
    const row = await editSetting(user, "TLS certificate source");
    expect(row.queryByRole("button", { name: "Save" })).toBeNull();
    await user.click(row.getByRole("button", { name: "Use default" }));
    await user.click(row.getByRole("button", { name: "Save" }));
    expect(api.calls.find((call) => call.method === "PATCH")?.body).toEqual({ metadata: { resourceVersion: "100" }, spec: { value: "" } });
  });

  it("retains the draft and shows the server validation message when a save fails", async () => {
    const user = userEvent.setup();
    render({ patch: { status: 422, body: { kind: "Status", reason: "Invalid", message: "value below minimum 5m0s" } } });
    await setting("Platform hostname");
    await chooseCategory(user, /^Sign-in and sessions/);
    const row = await editSetting(user, "Session lifetime");
    const input = row.getByRole("textbox") as HTMLInputElement;
    await user.type(input, "1s");
    await user.click(row.getByRole("button", { name: "Save" }));
    expect(await row.findByText("value below minimum 5m0s")).toBeTruthy();
    expect(input.value).toBe("1s");
    expect(row.getByRole("button", { name: "Save" })).toBeTruthy();
  });

  it("warns before changing the hostname", async () => {
    const user = userEvent.setup();
    const { api } = render();
    const row = await editSetting(user, "Platform hostname");
    const input = row.getByRole("textbox");
    await user.clear(input);
    await user.type(input, "new.example.test");
    await user.click(row.getByRole("button", { name: "Save" }));
    const confirm = await screen.findByRole("alertdialog", { name: "Change the platform host" });
    expect(within(confirm).getByText(/sign in again/)).toBeTruthy();
    expect(api.calls.some((call) => call.method === "PATCH")).toBe(false);
    await user.click(within(confirm).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((call) => call.method === "PATCH")).toBe(true));
  });

  it("only opens the multiline or enum editor on demand", async () => {
    const user = userEvent.setup();
    render();
    const certs = await setting("Additional trusted certificates");
    expect(certs.getByText("Not set")).toBeTruthy();
    expect(certs.queryByRole("textbox")).toBeNull();
    const row = await editSetting(user, "Additional trusted certificates");
    const area = row.getByRole("textbox");
    expect(area.tagName).toBe("TEXTAREA");
    expect(area.getAttribute("dir")).toBe("ltr");
    expect((await editSetting(user, "TLS certificate source")).getByRole("combobox")).toBeTruthy();
  });

  it("uses named permissions and renders plain values for denied rows", async () => {
    const user = userEvent.setup();
    const { api } = render({ allow: (check) => check.verb !== "patch" || check.name === "platform.host" });
    await screen.findByRole("button", { name: "Edit Platform hostname" });
    const tls = await setting("TLS certificate source");
    expect(tls.getAllByText("SelfSigned")[0]?.tagName).toBe("BDI");
    expect(tls.queryByRole("textbox")).toBeNull();
    expect(tls.queryByRole("combobox")).toBeNull();
    expect(tls.queryByRole("button", { name: /Edit|Save/ })).toBeNull();
    await chooseCategory(user, /^Kata runtime/);
    const kata = await setting("Enable Kata at install");
    expect(kata.getByText("Enabled")).toBeTruthy();
    expect(kata.queryByRole("switch")).toBeNull();
    expect(api.calls.some((call) => call.method === "PATCH")).toBe(false);
  });
});
