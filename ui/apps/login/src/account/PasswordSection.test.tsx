import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import { dirOf, locales } from "../i18n";
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

const currentLabels = { en: "Current password", fa: "رمز عبور فعلی", ar: "كلمة المرور الحالية" };
const newLabels = { en: "New password", fa: "رمز عبور جدید", ar: "كلمة المرور الجديدة" };
const submits = { en: "Change password", fa: "تغییر رمز عبور", ar: "تغيير كلمة المرور" };
const successes = { en: "Password changed", fa: "رمز عبور تغییر کرد", ar: "تم تغيير كلمة المرور" };

describe("PasswordSection", () => {
  beforeEach(() => {
    vi.mocked(api.changePassword).mockReset();
    vi.mocked(api.changePassword).mockResolvedValue(undefined);
  });

  for (const locale of locales) {
    it(`renders in ${locale} and submits with the CSRF token`, async () => {
      renderWithLocale(<PasswordSection csrf="csrf-acct" onSessionExpired={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      fireEvent.change(screen.getByLabelText(currentLabels[locale]), { target: { value: "old-pw" } });
      fireEvent.change(screen.getByLabelText(newLabels[locale]), { target: { value: "new-pw" } });
      fireEvent.click(screen.getByText(submits[locale]));
      await waitFor(() => {
        expect(api.changePassword).toHaveBeenCalledWith("old-pw", "new-pw", "csrf-acct");
      });
      expect(await screen.findByText(successes[locale])).not.toBeNull();
    });
  }

  it("sends the user to login when the server answers no_session", async () => {
    vi.mocked(api.changePassword).mockRejectedValue(new api.ApiError("no_session"));
    const onSessionExpired = vi.fn();
    renderWithLocale(<PasswordSection csrf="csrf-acct" onSessionExpired={onSessionExpired} />, "en");
    fireEvent.change(screen.getByLabelText(currentLabels.en), { target: { value: "old-pw" } });
    fireEvent.change(screen.getByLabelText(newLabels.en), { target: { value: "new-pw" } });
    fireEvent.click(screen.getByText(submits.en));
    await waitFor(() => {
      expect(onSessionExpired).toHaveBeenCalledTimes(1);
    });
  });

  it("shows the rate-limit message when the server answers rate_limited", async () => {
    vi.mocked(api.changePassword).mockRejectedValue(new api.ApiError("rate_limited"));
    renderWithLocale(<PasswordSection csrf="csrf-acct" onSessionExpired={() => {}} />, "en");
    fireEvent.change(screen.getByLabelText(currentLabels.en), { target: { value: "old-pw" } });
    fireEvent.change(screen.getByLabelText(newLabels.en), { target: { value: "new-pw" } });
    fireEvent.click(screen.getByText(submits.en));
    expect(await screen.findByRole("alert")).toHaveProperty(
      "textContent",
      "Too many attempts. Wait a moment and try again.",
    );
  });
});
