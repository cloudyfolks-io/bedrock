import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ConfirmDeleteDialog, ConfirmSimpleDialog } from "./ConfirmDialogs";
import { renderPanel } from "../test/renderPanel";

describe("ConfirmDeleteDialog", () => {
  it("keeps the confirm button disabled until the exact name is typed", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    renderPanel(<ConfirmDeleteDialog name="web-1" kind="virtual machine" open onConfirm={onConfirm} onCancel={() => {}} />);
    const confirm = await screen.findByRole("button", { name: "Delete" });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);
    await user.type(screen.getByLabelText("Name"), "Web-1");
    expect((confirm as HTMLButtonElement).disabled).toBe(true);
    await user.clear(screen.getByLabelText("Name"));
    await user.type(screen.getByLabelText("Name"), "web-1");
    expect((confirm as HTMLButtonElement).disabled).toBe(false);
    await user.click(confirm);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("cancels on Escape", async () => {
    const user = userEvent.setup();
    const onCancel = vi.fn();
    const onConfirm = vi.fn();
    renderPanel(<ConfirmDeleteDialog name="web-1" kind="virtual machine" open onConfirm={onConfirm} onCancel={onCancel} />);
    await screen.findByRole("dialog");
    await user.keyboard("{Escape}");
    expect(onCancel).toHaveBeenCalled();
    expect(onConfirm).not.toHaveBeenCalled();
  });
});

describe("ConfirmSimpleDialog", () => {
  it("confirms on one click and cancels on Escape", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    const view = renderPanel(
      <ConfirmSimpleDialog title="Stop" description="The machine stops." confirmLabel="Stop it" open onConfirm={onConfirm} onCancel={onCancel} />,
    );
    await user.click(await screen.findByRole("button", { name: "Stop it" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    view.unmount();
    const onConfirm2 = vi.fn();
    renderPanel(<ConfirmSimpleDialog title="Stop" description="d" confirmLabel="Stop it" open onConfirm={onConfirm2} onCancel={onCancel} />);
    await screen.findByRole("alertdialog");
    await user.keyboard("{Escape}");
    expect(onConfirm2).not.toHaveBeenCalled();
  });
});
