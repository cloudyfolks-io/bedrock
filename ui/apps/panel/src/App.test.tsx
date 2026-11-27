import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "./App";
import { createFakeApi } from "./test/fakeApi";
import { defaultSession } from "./test/renderPanel";
import { wizardApi } from "./wizard/testSupport";

describe("App", () => {
  afterEach(() => {
    localStorage.clear();
    window.history.pushState({}, "", "/");
  });

  it("renders the shell and guards a wizard draft on browser Back", async () => {
    const user = userEvent.setup();
    createFakeApi(wizardApi({ "GET /proxy/v1/session": { body: { ...defaultSession, admin: false } } }), () => true).install();
    render(<App />);
    expect(await screen.findByRole("link", { name: /sCloud/ })).toBeTruthy();
    expect(document.documentElement.dir).toBe("ltr");

    await user.click(await screen.findByRole("link", { name: "Create" }, { timeout: 10000 }));
    await user.type(await screen.findByRole("textbox", { name: "Name" }, { timeout: 10000 }), "draft-vm");
    window.history.back();
    const dialog = await screen.findByRole("alertdialog");
    await waitFor(() => expect(window.location.pathname).toBe("/p/web/vms/new"));
    await user.click(within(dialog).getByRole("button", { name: "Discard" }));
    await waitFor(() => expect(window.location.pathname).toBe("/p/web/vms"));
    expect(await screen.findByRole("heading", { level: 1, name: "Virtual machines" })).toBeTruthy();
  });
});
