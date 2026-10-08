import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { Check, KubeList } from "../../api/types";
import addons from "../../fixtures/addons.json";
import { renderPanel } from "../../test/renderPanel";
import AddonsPage from "./AddonsPage";

const path = "/apis/bedrock.cloudyfolks.io/v1alpha1/addons";

function render(allow: (check: Check) => boolean = () => true) {
  return renderPanel(
    <Routes>
      <Route path="/admin/addons" element={<AddonsPage />} />
    </Routes>,
    { route: "/admin/addons", allow, api: { [`GET ${path}`]: { body: addons as unknown as KubeList }, [`PATCH ${path}/*`]: { body: {} } } },
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
  });

  it("shows the Ready condition and a preflight message", async () => {
    render();
    const loki = (await screen.findByRole("region", { name: "loki" })) as HTMLElement;
    expect(within(loki).getByText("Not ready")).toBeTruthy();
    expect(within(loki).getByText(/kata needs nested virtualization/)).toBeTruthy();
    const kata = screen.getByRole("region", { name: "kata" });
    expect(within(kata).getByText("Ready")).toBeTruthy();
  });

  it("TestAddonValuesEditor refuses invalid JSON and saves valid JSON", async () => {
    const user = userEvent.setup();
    const { api } = render();
    const loki = (await screen.findByRole("region", { name: "loki" })) as HTMLElement;
    const area = within(loki).getByLabelText("loki values");
    await user.clear(area);
    await user.click(area);
    await user.paste("{oops");
    await user.click(within(loki).getByRole("button", { name: "Save values" }));
    expect(await within(loki).findByText("This is not valid JSON")).toBeTruthy();
    expect(api.calls.some((call) => call.method === "PATCH")).toBe(false);
    await user.clear(area);
    await user.click(area);
    await user.paste('{"retention": "7d"}');
    await user.click(within(loki).getByRole("button", { name: "Save values" }));
    await waitFor(() => expect(api.calls.some((call) => call.method === "PATCH")).toBe(true));
    expect(api.calls.find((call) => call.method === "PATCH")?.body).toEqual({ spec: { values: { retention: "7d" } } });
  });

  it("hides the switch without update permission", async () => {
    render((check) => check.verb !== "update");
    const loki = (await screen.findByRole("region", { name: "loki" })) as HTMLElement;
    await waitFor(() => expect(within(loki).queryByRole("switch")).toBeNull());
    expect(within(loki).queryByRole("button", { name: "Save values" })).toBeNull();
  });
});
