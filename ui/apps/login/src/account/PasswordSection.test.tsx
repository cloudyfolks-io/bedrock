import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import { dirOf, locales } from "../i18n";
import { expectedMessage } from "../test/expectedMessage";
import { renderWithLocale } from "../test/renderWithLocale";
import { PasswordSection } from "./PasswordSection";

vi.mock("../api", () => ({
  changePassword: vi.fn(),
  ApiError: class ApiError extends Error {
    code: string;
    constructor(code: string) {
      super(code);
      this.code = code;
    }
  },
}));

describe("PasswordSection", () => {
  beforeEach(() => {
    vi.mocked(api.changePassword).mockReset();
    vi.mocked(api.changePassword).mockResolvedValue(undefined);
  });

  for (const locale of locales) {
    it(`renders in ${locale} and submits with the CSRF token`, async () => {
      renderWithLocale(<PasswordSection csrf="csrf-acct" onSessionExpired={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      fireEvent.change(screen.getByLabelText(expectedMessage(locale, "account.password.current")), { target: { value: "old-pw" } });
      fireEvent.change(screen.getByLabelText(expectedMessage(locale, "account.password.new")), { target: { value: "new-pw" } });
      fireEvent.click(screen.getByText(expectedMessage(locale, "account.password.submit")));
      await waitFor(() => {
        expect(api.changePassword).toHaveBeenCalledWith("old-pw", "new-pw", "csrf-acct");
      });
      expect(await screen.findByText(expectedMessage(locale, "account.password.success"))).not.toBeNull();
    });
  }

  it("sends the user to login when the server answers no_session", async () => {
    vi.mocked(api.changePassword).mockRejectedValue(new api.ApiError("no_session"));
    const onSessionExpired = vi.fn();
    renderWithLocale(<PasswordSection csrf="csrf-acct" onSessionExpired={onSessionExpired} />, "en");
    fireEvent.change(screen.getByLabelText(expectedMessage("en", "account.password.current")), { target: { value: "old-pw" } });
    fireEvent.change(screen.getByLabelText(expectedMessage("en", "account.password.new")), { target: { value: "new-pw" } });
    fireEvent.click(screen.getByText(expectedMessage("en", "account.password.submit")));
    await waitFor(() => {
      expect(onSessionExpired).toHaveBeenCalledTimes(1);
    });
  });

  it("shows the rate-limit message when the server answers rate_limited", async () => {
    vi.mocked(api.changePassword).mockRejectedValue(new api.ApiError("rate_limited"));
    renderWithLocale(<PasswordSection csrf="csrf-acct" onSessionExpired={() => {}} />, "en");
    fireEvent.change(screen.getByLabelText(expectedMessage("en", "account.password.current")), { target: { value: "old-pw" } });
    fireEvent.change(screen.getByLabelText(expectedMessage("en", "account.password.new")), { target: { value: "new-pw" } });
    fireEvent.click(screen.getByText(expectedMessage("en", "account.password.submit")));
    expect(await screen.findByRole("alert")).toHaveProperty(
      "textContent",
      expectedMessage("en", "login.error.rate_limited"),
    );
  });
});
