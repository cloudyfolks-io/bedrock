import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "./api";
import { App } from "./App";
import { locales } from "./i18n";
import { expectedMessage } from "./test/expectedMessage";

vi.mock("./api", () => ({
  challenge: vi.fn().mockResolvedValue({ type: "username", csrf: "csrf-1" }),
  start: vi.fn().mockResolvedValue({ type: "username", csrf: "csrf-1" }),
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
});
