import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import { useHttp } from "../../api/HttpProvider";
import { usePendingWrites } from "../../api/pendingWrites";
import type { Check, KubeList } from "../../api/types";
import addons from "../../fixtures/addons.json";
import type { FakeRoute } from "../../test/fakeApi";
import { renderPanel } from "../../test/renderPanel";
import AddonsPage from "./AddonsPage";

const path = "/apis/bedrock.cloudyfolks.io/v1alpha1/addons";

function PendingWrites() {
  const writes = usePendingWrites(useHttp());
  return <output>{writes.map(({ kind, name }) => `${kind}:${name}`).join(",")}</output>;
}

function render(allow: (check: Check) => boolean = () => true, patch: FakeRoute = { body: {} }) {
  return renderPanel(
    <Routes>
      <Route path="/admin/addons" element={<><AddonsPage /><PendingWrites /></>} />
    </Routes>,
    { route: "/admin/addons", allow, api: { [`GET ${path}`]: { body: addons as unknown as KubeList }, [`PATCH ${path}/*`]: patch } },
  );
}

describe("AddonsPage", () => {
  it("binds the switch to spec.enabled with a merge patch", async () => {
    const user = userEvent.setup();
    const { api } = render();
    const loki = (await screen.findByRole("region", { name: "loki" })) as HTMLElement;
    await user.click(within(loki).getByRole("switch", { name: "Enabled" }));
    await waitFor(() => expect(api.calls.some((call) => call.method === "PATCH")).toBe(true));
    const patch = api.calls.find((call) => call.method === "PATCH");
    expect(patch?.path).toBe(`${path}/loki`);
    expect(patch?.body).toEqual({ spec: { enabled: true } });
    expect(patch?.headers.get("Content-Type")).toBe("application/merge-patch+json");
    expect(await screen.findByText("Request accepted")).toBeTruthy();
  });

  it("separates enablement from controller health and names missing values", async () => {
    render();
    const loki = (await screen.findByRole("region", { name: "loki" })) as HTMLElement;
    expect(within(loki).getByText("Not ready")).toBeTruthy();
    expect(within(loki).getByText("Disabled")).toBeTruthy();
    expect(within(loki).getByText("the addon is disabled")).toBeTruthy();
    const kata = screen.getByRole("region", { name: "kata" });
    expect(within(kata).getByText("Ready")).toBeTruthy();
    expect(within(kata).getByText("No custom values")).toBeTruthy();
    expect(within(kata).queryByLabelText("kata values")).toBeNull();
    expect(loki.querySelector("details")?.open).toBe(false);
  });

  it("TestAddonValuesEditor refuses invalid JSON and saves valid JSON", async () => {
    const user = userEvent.setup();
    let release = () => {};
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const { api } = render(() => true, async () => {
      await gate;
      return { body: {} };
    });
    const loki = (await screen.findByRole("region", { name: "loki" })) as HTMLElement;
    await user.click(within(loki).getByText("Advanced values (JSON)"));
    expect(loki.querySelector("details")?.open).toBe(true);
    const area = within(loki).getByLabelText("loki values");
    await user.clear(area);
    await user.click(area);
    await user.paste("{oops");
    await user.click(within(loki).getByRole("button", { name: "Save values" }));
    expect(await within(loki).findByText("This is not valid JSON")).toBeTruthy();
    expect(api.calls.some((call) => call.method === "PATCH")).toBe(false);
    await user.clear(area);
    await user.click(area);
    await user.paste('{"persistence.size": "40Gi"}');
    await user.click(within(loki).getByRole("button", { name: "Save values" }));
    expect(await screen.findByText("update:loki")).toBeTruthy();
    expect(screen.queryByText("Request accepted")).toBeNull();
    await waitFor(() => expect(api.calls.some((call) => call.method === "PATCH")).toBe(true));
    const patch = api.calls.find((call) => call.method === "PATCH");
    expect(patch?.headers.get("Content-Type")).toBe("application/json-patch+json");
    expect(patch?.body).toEqual([
      { op: "test", path: "/metadata/resourceVersion", value: "100" },
      { op: "add", path: "/spec/values", value: { "persistence.size": "40Gi" } },
    ]);
    release();
    expect(await screen.findByText("Request accepted")).toBeTruthy();
  });

  it("hides the switch without patch permission", async () => {
    const user = userEvent.setup();
    render((check) => check.verb !== "patch");
    const loki = (await screen.findByRole("region", { name: "loki" })) as HTMLElement;
    await user.click(within(loki).getByText("Advanced values (JSON)"));
    await waitFor(() => expect(within(loki).queryByRole("switch")).toBeNull());
    expect((within(loki).getByLabelText("loki values") as HTMLTextAreaElement).readOnly).toBe(true);
    expect(within(loki).queryByRole("button", { name: "Save values" })).toBeNull();
  });

  it("offers an editor for addons with no custom values only on request", async () => {
    const user = userEvent.setup();
    render();
    const kata = within(await screen.findByRole("region", { name: "kata" }));
    await user.click(await kata.findByText("Advanced values (JSON)"));
    expect(kata.queryByLabelText("kata values")).toBeNull();
    await user.click(kata.getByRole("button", { name: "Add custom values" }));
    expect((kata.getByLabelText("kata values") as HTMLTextAreaElement).value).toBe("");
  });
});
