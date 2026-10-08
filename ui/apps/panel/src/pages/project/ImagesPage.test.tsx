import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { KubeList } from "../../api/types";
import clusterImages from "../../fixtures/clusterimages.json";
import images from "../../fixtures/images.json";
import { renderPanel } from "../../test/renderPanel";
import ImagesPage from "./ImagesPage";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const api = "/apis/bedrock.cloudyfolks.io/v1alpha1";

function render() {
  return renderPanel(
    <Routes>
      <Route path="/p/:project/images" element={<ImagesPage />} />
    </Routes>,
    {
      route: "/p/web/images",
      api: { "GET /proxy/v1/projects": { body: projects }, [`GET ${api}/namespaces/acme-web/images`]: { body: images as unknown as KubeList }, [`GET ${api}/clusterimages`]: { body: clusterImages as unknown as KubeList } },
    },
  );
}

describe("ImagesPage", () => {
  it("lists project images with type, phase and digest", async () => {
    render();
    const disk = (await screen.findByRole("link", { name: "custom-disk" })).closest("tr") as HTMLElement;
    expect(within(disk).getByText("disk")).toBeTruthy();
    expect(within(disk).getByText("Ready")).toBeTruthy();
    expect(disk.querySelector("bdi")?.textContent).toBeTruthy();
    const iso = screen.getByRole("link", { name: "installer" }).closest("tr") as HTMLElement;
    expect(within(iso).getByText("Waiting for upload")).toBeTruthy();
  });

  it("shows the catalog read only with no links or actions", async () => {
    const user = userEvent.setup();
    render();
    await user.click(await screen.findByRole("tab", { name: "Catalog" }));
    expect(await screen.findByText("ubuntu-24-04")).toBeTruthy();
    expect(screen.queryByRole("link", { name: "ubuntu-24-04" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Actions for ubuntu/ })).toBeNull();
    expect(screen.getAllByText("Importing").length).toBeGreaterThan(0);
  });
});
