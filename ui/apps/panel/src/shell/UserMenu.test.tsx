import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { renderPanel } from "../test/renderPanel";
import { UserMenu } from "./UserMenu";

it("shows the signed-in name in a portaled menu and restores keyboard focus on Escape", async () => {
  const user = userEvent.setup();
  const { container } = renderPanel(<UserMenu />);
  const trigger = screen.getByRole("button", { name: "User menu" });
  expect(trigger.textContent).toBe("");

  await user.tab();
  expect(document.activeElement).toBe(trigger);
  await user.keyboard("{ArrowDown}");

  const menu = await screen.findByRole("menu");
  expect(container.contains(menu)).toBe(false);
  expect(within(menu).getByRole("group", { name: "Administrator" })).toBeTruthy();
  const account = within(menu).getByRole("menuitem", { name: "Account" });
  expect(account.getAttribute("href")).toBe("/account");
  await waitFor(() => expect(document.activeElement).toBe(account));

  await user.keyboard("{ArrowDown}");
  expect(document.activeElement).toBe(within(menu).getByRole("menuitem", { name: "Download kubeconfig" }));
  await user.keyboard("{Escape}");
  await waitFor(() => expect(document.activeElement).toBe(trigger));
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
});

it("keeps the user on the page when signing out fails", async () => {
  const user = userEvent.setup();
  const assign = vi.fn();
  const { api } = renderPanel(<UserMenu />, { assign, api: { "POST /oauth/logout": { status: 502 } } });

  const trigger = screen.getByRole("button", { name: "User menu" });
  await user.tab();
  await user.keyboard("{ArrowDown}");
  await screen.findByRole("menuitem", { name: "Sign out" });
  await user.keyboard("{End}{Enter}");

  expect(await screen.findByText("Could not sign out")).toBeTruthy();
  expect(assign).not.toHaveBeenCalled();
  await waitFor(() => expect(api.calls.some(({ method, path }) => method === "GET" && path === "/proxy/v1/session")).toBe(true));
  await waitFor(() => expect(trigger.hasAttribute("aria-disabled")).toBe(false));
  expect(document.activeElement).toBe(trigger);
});
