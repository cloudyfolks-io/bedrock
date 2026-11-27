import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { renderPanel } from "../test/renderPanel";
import { UploadIsoDialog } from "./UploadIsoDialog";

it("acknowledges ISO creation before the upload is ready", async () => {
  const user = userEvent.setup();
  let release = () => {};
  const gate = new Promise<void>((resolve) => { release = resolve; });
  const uploaded = vi.fn();
  const path = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/images";
  const metadata = { name: "installer", namespace: "acme-web" };
  renderPanel(<UploadIsoDialog namespace="acme-web" onClose={() => {}} onUploaded={uploaded} />, {
    api: {
      [`POST ${path}`]: async () => {
        await gate;
        return { status: 201, body: { metadata } };
      },
      [`GET ${path}/installer`]: { body: { metadata, status: { phase: "Pending" } } },
    },
  });
  await user.type(screen.getByRole("textbox", { name: "Name" }), "installer");
  const create = screen.getByRole("button", { name: "Create" }) as HTMLButtonElement;
  await user.click(create);
  expect(create.disabled).toBe(true);
  expect(screen.queryByText("Request accepted")).toBeNull();
  release();
  expect(await screen.findByText("Request accepted")).toBeTruthy();
  expect(screen.getByText("Preparing the upload")).toBeTruthy();
  expect(screen.queryByText("Disk image file")).toBeNull();
  expect(uploaded).not.toHaveBeenCalled();
});
