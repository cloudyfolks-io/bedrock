import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "./api";
import { App } from "./App";
import { locales } from "./i18n";
import { expectedMessage } from "./test/expectedMessage";

vi.mock("./api", () => ({
  challenge: vi.fn().mockResolvedValue({ type: "username", csrf: "csrf-1" }),
  start: vi.fn().mockResolvedValue({ type: "username", csrf: "csrf-1" }),
  answer: vi.fn(),
  ApiError: class ApiError extends Error {
    code: string;
    constructor(code: string) {
      super(code);
      this.code = code;
    }
  },
}));

function endonym(locale: string): string {
  return new Intl.DisplayNames([locale], { type: "language" }).of(locale) ?? locale;
}

describe("App", () => {
  beforeEach(() => {
    window.history.pushState({}, "", "/login/");
    localStorage.clear();
  });

  it("shows each language's own name and switches dir when changed", async () => {
    render(<App />);
    await waitFor(() => expect(document.documentElement.dir).toBe("ltr"));
    fireEvent.click(screen.getByText(endonym("en")));
    const menu = screen.getByRole("menu");
    for (const locale of locales) {
      within(menu).getByText(endonym(locale));
    }
    fireEvent.click(within(menu).getByText(endonym("fa")));
    await waitFor(() => expect(document.documentElement.dir).toBe("rtl"));
  });

  it("shows the error screen instead of a blank page when the first load fails", async () => {
    vi.mocked(api.challenge).mockRejectedValueOnce(new Error("network down"));
    render(<App />);
    expect(await screen.findByText(expectedMessage("en", "login.error.title"))).not.toBeNull();
  });

  it("starts sAxess, checks only on request, and continues to MFA with fresh CSRF", async () => {
    vi.mocked(api.challenge).mockResolvedValueOnce({
      type: "username",
      csrf: "csrf-1",
      saxess: true,
      providers: [{ name: "dex", displayName: "Dadehat SSO", type: "oidc" }],
    });
    vi.mocked(api.answer)
      .mockResolvedValueOnce({ type: "saxess", csrf: "csrf-2" })
      .mockResolvedValueOnce({ type: "saxess", csrf: "csrf-2" })
      .mockResolvedValueOnce({ type: "totp", csrf: "csrf-3", methods: ["totp"] })
      .mockResolvedValueOnce({ type: "done" });

    render(<App />);
    const start = await screen.findByRole("button", { name: expectedMessage("en", "login.saxess.start") }) as HTMLButtonElement;
    expect(start.disabled).toBe(true);
    expect(screen.getByRole("button", { name: "Dadehat SSO" })).not.toBeNull();
    expect(screen.getByRole("button", { name: expectedMessage("en", "login.username.continue") })).not.toBeNull();
    fireEvent.change(screen.getByLabelText(expectedMessage("en", "login.username.label")), { target: { value: "alice" } });
    fireEvent.click(start);
    expect(api.answer).toHaveBeenNthCalledWith(1, { type: "saxess-start", username: "alice" }, "csrf-1");

    const check = await screen.findByRole("button", { name: expectedMessage("en", "login.saxess.check") });
    expect(screen.getByText(expectedMessage("en", "login.saxess.instructions"))).not.toBeNull();
    expect(api.answer).toHaveBeenCalledTimes(1);
    fireEvent.click(check);
    expect(await screen.findByText(expectedMessage("en", "login.saxess.pending"))).not.toBeNull();
    expect(api.answer).toHaveBeenNthCalledWith(2, { type: "saxess" }, "csrf-2");
    fireEvent.click(check);
    expect(api.answer).toHaveBeenNthCalledWith(3, { type: "saxess" }, "csrf-2");

    const verify = await screen.findByRole("button", { name: expectedMessage("en", "login.totp.submit") });
    fireEvent.paste(screen.getByLabelText(`${expectedMessage("en", "login.totp.label")} 1`), {
      clipboardData: { getData: () => "123456" },
    });
    fireEvent.click(verify);
    expect(api.answer).toHaveBeenNthCalledWith(4, { type: "totp", code: "123456" }, "csrf-3");
    expect(await screen.findByText(expectedMessage("en", "login.done"))).not.toBeNull();
  });
});
