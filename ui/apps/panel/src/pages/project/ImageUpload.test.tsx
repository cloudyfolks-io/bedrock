import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { beforeEach, describe, expect, it } from "vitest";
import type { KubeList } from "../../api/types";
import images from "../../fixtures/images.json";
import { FakeXhr } from "../../test/fakeXhr";
import { renderPanel } from "../../test/renderPanel";
import ImagesPage from "./ImagesPage";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const api = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web";
const grant = { url: "https://upload.e2e.bedrock.test/v1beta1/upload-async", token: "tok-secret-1", expiresAt: "2026-10-08T13:00:00Z" };

function render(upload: () => { status?: number; body?: unknown } = () => ({ body: grant })) {
  return renderPanel(
    <Routes>
      <Route path="/p/:project/images" element={<ImagesPage />} />
    </Routes>,
    {
      route: "/p/web/images",
      api: {
        "GET /proxy/v1/projects": { body: projects },
        [`GET ${api}/images`]: { body: images as unknown as KubeList },
        "GET /apis/bedrock.cloudyfolks.io/v1alpha1/clusterimages": { body: { items: [], metadata: { resourceVersion: "1" } } },
        [`POST ${api}/images/installer/upload`]: upload,
      },
    },
  );
}

async function openUpload(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "Actions for installer" }));
  await user.click(await screen.findByRole("menuitem", { name: "Upload" }));
  const dialog = await screen.findByRole("dialog");
  const input = dialog.querySelector('input[type="file"]') as HTMLInputElement;
  await user.upload(input, new File(["abc"], "debian.iso", { type: "application/octet-stream" }));
  return dialog;
}

describe("ImageUpload", () => {
  beforeEach(() => {
    FakeXhr.install();
    localStorage.clear();
    sessionStorage.clear();
  });

  it("TestUploadFlow asks for a token and then posts the file cross origin", async () => {
    const user = userEvent.setup();
    const { api: fake, queryClient } = render();
    const dialog = await openUpload(user);
    expect(dialog.querySelector("input[type=file]")?.getAttribute("accept")).toBe(".iso,.img,.qcow2");
    await user.click(within(dialog).getByRole("button", { name: "Upload" }));
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1));
    const post = fake.calls.find((call) => call.path.endsWith("/installer/upload"));
    expect(post?.method).toBe("POST");
    expect(post?.headers.get("X-CSRF-Token")).toBeTruthy();
    const xhr = FakeXhr.latest();
    expect(xhr.method).toBe("POST");
    expect(xhr.url).toBe(grant.url);
    expect(xhr.headers.Authorization).toBe("Bearer tok-secret-1");
    expect(xhr.headers["X-CSRF-Token"]).toBeUndefined();
    expect(xhr.withCredentials).toBe(false);
    expect((xhr.body as File).name).toBe("debian.iso");
    expect(xhr.url).not.toContain("tok-secret-1");
    xhr.progress(1, 3);
    const bar = await screen.findByRole("progressbar", { name: "Upload progress" });
    expect(bar.getAttribute("aria-valuenow")).toBe("1");
    xhr.finish(200);
    expect(await screen.findByText("Upload finished")).toBeTruthy();
    expect(JSON.stringify(Object.entries(localStorage))).not.toContain("tok-secret-1");
    expect(JSON.stringify(Object.entries(sessionStorage))).not.toContain("tok-secret-1");
    expect(JSON.stringify(queryClient.getQueryCache().getAll().map((query) => query.state.data))).not.toContain("tok-secret-1");
  });

  it("offers a retry with a new token after a network error", async () => {
    const user = userEvent.setup();
    let count = 0;
    render(() => {
      count += 1;
      return { body: { ...grant, token: `tok-${count}` } };
    });
    const dialog = await openUpload(user);
    await user.click(within(dialog).getByRole("button", { name: "Upload" }));
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1));
    FakeXhr.latest().fail();
    await user.click(await within(dialog).findByRole("button", { name: "Retry" }));
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(2));
    expect(FakeXhr.latest().headers.Authorization).toBe("Bearer tok-2");
  });

  it("cancels by aborting the request", async () => {
    const user = userEvent.setup();
    render();
    const dialog = await openUpload(user);
    await user.click(within(dialog).getByRole("button", { name: "Upload" }));
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1));
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(FakeXhr.latest().aborted).toBe(true);
  });

  it("shows the error text of a 409 from the subresource", async () => {
    const user = userEvent.setup();
    render(() => ({ status: 409, body: { error: "image is not waiting for upload" } }));
    const dialog = await openUpload(user);
    await user.click(within(dialog).getByRole("button", { name: "Upload" }));
    expect(await within(dialog).findByText("image is not waiting for upload")).toBeTruthy();
    expect(FakeXhr.instances).toHaveLength(0);
  });

  it.each(["https://evil.test/v1beta1/upload-async", "https://upload.e2e.bedrock.test.evil/v1beta1/upload-async", "http://upload.e2e.bedrock.test/v1beta1/upload-async", "https://upload.e2e.bedrock.test:8443/v1beta1/upload-async", "https://user@upload.e2e.bedrock.test/x"])(
    "refuses to send the token to %s",
    async (url) => {
      const user = userEvent.setup();
      render(() => ({ body: { ...grant, url } }));
      const dialog = await openUpload(user);
      await user.click(within(dialog).getByRole("button", { name: "Upload" }));
      expect(await within(dialog).findByText(/not on this platform/)).toBeTruthy();
      expect(FakeXhr.instances).toHaveLength(0);
    },
  );
});
