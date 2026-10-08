import { useState } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DirectionProvider } from "@base-ui/react/direction-provider";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AlertDialog, Dialog, MenuButton, ToastProvider, Tooltip, useToast } from "./index";

function DialogFixture({ onOpenChange }: { onOpenChange?: (open: boolean) => void }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Open
      </button>
      <Dialog
        open={open}
        onOpenChange={(next) => {
          onOpenChange?.(next);
          setOpen(next);
        }}
        title="Edit host"
        description="Change roles"
        actions={<button type="button">Apply</button>}
      >
        <input aria-label="Role" />
      </Dialog>
    </>
  );
}

describe("Dialog", () => {
  it("is labelled by its title and keeps focus inside", async () => {
    const user = userEvent.setup();
    render(<DialogFixture />);
    await user.click(screen.getByRole("button", { name: "Open" }));
    const dialog = await screen.findByRole("dialog", { name: "Edit host" });
    expect(dialog.getAttribute("aria-labelledby")).toBeTruthy();
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    for (let index = 0; index < 6; index += 1) {
      await user.tab();
      await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    }
    expect(screen.getByText("Change roles")).toBeTruthy();
  });

  it("closes on Escape and returns focus to the opener", async () => {
    const user = userEvent.setup();
    render(<DialogFixture />);
    const opener = screen.getByRole("button", { name: "Open" });
    await user.click(opener);
    await screen.findByRole("dialog");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.activeElement).toBe(opener);
  });
});

function AlertFixture({ onConfirm, tone }: { onConfirm: () => void; tone?: "danger" | "neutral" }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Open
      </button>
      <AlertDialog
        open={open}
        onOpenChange={setOpen}
        title="Delete"
        description="This cannot be undone"
        confirmLabel="Delete it"
        cancelLabel="Keep it"
        onConfirm={onConfirm}
        tone={tone ?? "danger"}
      />
    </>
  );
}

describe("AlertDialog", () => {
  it("cancels on Escape without confirming", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(<AlertFixture onConfirm={onConfirm} />);
    await user.click(screen.getByRole("button", { name: "Open" }));
    await screen.findByRole("alertdialog");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("confirms once and starts on cancel for the danger tone", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(<AlertFixture onConfirm={onConfirm} />);
    await user.click(screen.getByRole("button", { name: "Open" }));
    const cancel = await screen.findByRole("button", { name: "Keep it" });
    await waitFor(() => expect(document.activeElement).toBe(cancel));
    await user.click(screen.getByRole("button", { name: "Delete it" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });
});

function ToastFixture() {
  const toast = useToast();
  return (
    <>
      <button type="button" onClick={() => toast.show({ tone: "success", title: "Saved" })}>
        ok
      </button>
      <button type="button" onClick={() => toast.show({ tone: "danger", title: "Failed", description: "Try again" })}>
        bad
      </button>
      <button type="button" onClick={() => toast.show({ tone: "info", title: "Info" })}>
        info
      </button>
    </>
  );
}

describe("ToastProvider", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("announces success in a status region and danger as an alert", async () => {
    const user = userEvent.setup();
    render(
      <ToastProvider>
        <ToastFixture />
      </ToastProvider>,
    );
    await user.click(screen.getByRole("button", { name: "ok" }));
    expect(screen.getByRole("status").textContent).toContain("Saved");
    await user.click(screen.getByRole("button", { name: "bad" }));
    expect(screen.getByRole("alert").textContent).toContain("Failed");
  });

  it("removes a success toast after 5 seconds and keeps a danger toast until dismissed", () => {
    vi.useFakeTimers();
    render(
      <ToastProvider>
        <ToastFixture />
      </ToastProvider>,
    );
    act(() => screen.getByRole("button", { name: "ok" }).click());
    act(() => screen.getByRole("button", { name: "bad" }).click());
    act(() => {
      vi.advanceTimersByTime(5100);
    });
    expect(screen.queryByText("Saved")).toBeNull();
    expect(screen.getByText("Failed")).toBeTruthy();
    act(() => screen.getByRole("button", { name: "Dismiss" }).click());
    expect(screen.queryByText("Failed")).toBeNull();
  });

  it("shows at most four toasts", () => {
    render(
      <ToastProvider>
        <ToastFixture />
      </ToastProvider>,
    );
    for (let index = 0; index < 6; index += 1) {
      act(() => screen.getByRole("button", { name: "bad" }).click());
    }
    expect(screen.getAllByRole("alert")).toHaveLength(4);
  });
});

describe("Tooltip", () => {
  it("shows on focus, links aria-describedby and hides on Escape", async () => {
    const user = userEvent.setup();
    render(
      <Tooltip content="More about this">
        <button type="button">Help</button>
      </Tooltip>,
    );
    await user.tab();
    const tip = await screen.findByText("More about this");
    const trigger = screen.getByRole("button", { name: "Help" });
    expect(document.getElementById(trigger.getAttribute("aria-describedby") ?? "")).toBe(tip);
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByText("More about this")).toBeNull());
  });

  it("shows on hover", async () => {
    const user = userEvent.setup();
    render(
      <Tooltip content="Hover text">
        <button type="button">Help</button>
      </Tooltip>,
    );
    await user.hover(screen.getByRole("button", { name: "Help" }));
    expect(await screen.findByText("Hover text")).toBeTruthy();
  });
});

describe("MenuButton", () => {
  it("opens with Enter, moves with arrows and selects with Enter", async () => {
    const user = userEvent.setup();
    const first = vi.fn();
    const second = vi.fn();
    render(
      <MenuButton
        label="Actions"
        items={[
          { id: "one", label: "First", onSelect: first },
          { id: "two", label: "Second", onSelect: second },
        ]}
      />,
    );
    screen.getByRole("button", { name: "Actions" }).focus();
    await user.keyboard("{Enter}");
    await screen.findByRole("menuitem", { name: "First" });
    await user.keyboard("{ArrowDown}{Enter}");
    expect(second).toHaveBeenCalledTimes(1);
    expect(first).not.toHaveBeenCalled();
  });

  it("does not select a disabled item", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<MenuButton label="Actions" items={[{ id: "one", label: "First", onSelect, disabled: true }]} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "First" }));
    expect(onSelect).not.toHaveBeenCalled();
  });
});

describe("right to left", () => {
  it("renders every overlay in a right-to-left tree", async () => {
    const user = userEvent.setup();
    render(
      <DirectionProvider direction="rtl">
        <div dir="rtl">
          <ToastProvider>
            <ToastFixture />
            <Tooltip content="tip">
              <button type="button">Help</button>
            </Tooltip>
            <MenuButton label="Actions" items={[{ id: "a", label: "A", onSelect: () => {} }]} />
          </ToastProvider>
          <DialogFixture />
        </div>
      </DirectionProvider>,
    );
    await user.click(screen.getByRole("button", { name: "info" }));
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(await screen.findByRole("menuitem", { name: "A" })).toBeTruthy();
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "Open" }));
    expect(await screen.findByRole("dialog")).toBeTruthy();
  });
});
