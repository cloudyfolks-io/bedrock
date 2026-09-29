import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import fixtures from "../fixtures/challenges.json";
import { dirOf, locales } from "../i18n";
import { renderWithLocale } from "../test/renderWithLocale";
import type { Challenge } from "../types";
import { Totp } from "./Totp";

vi.mock("../api", () => ({ answer: vi.fn() }));

const challenge = fixtures.find((f) => f.name === "totp")!.challenge as Challenge;
const titles = { en: "Enter your authenticator code", fa: "کد برنامه احراز هویت خود را وارد کنید", ar: "أدخل رمز تطبيق المصادقة" };
const codeLabels = { en: "Authentication code 1", fa: "کد تأیید 1", ar: "رمز التحقق 1" };
const submits = { en: "Verify", fa: "تأیید", ar: "تحقق" };
const useRecovery = {
  en: "Use a recovery code instead",
  fa: "در عوض از کد بازیابی استفاده کنید",
  ar: "استخدم رمز الاسترداد بدلاً من ذلك",
};

describe("Totp", () => {
  beforeEach(() => {
    vi.mocked(api.answer).mockReset();
    vi.mocked(api.answer).mockResolvedValue({ type: "done", redirect: "/x" });
  });

  for (const locale of locales) {
    it(`renders in ${locale}, focuses the first digit and submits six typed digits with the CSRF token`, async () => {
      renderWithLocale(<Totp challenge={challenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(titles[locale])).not.toBeNull();
      expect(document.activeElement).toBe(screen.getByLabelText(codeLabels[locale]));
      const clipboardData = { getData: () => "123456" };
      fireEvent.paste(screen.getByLabelText(codeLabels[locale]), { clipboardData });
      fireEvent.click(screen.getByText(submits[locale]));
      await waitFor(() => {
        expect(api.answer).toHaveBeenCalledWith({ type: "totp", code: "123456" }, challenge.csrf);
      });
      expect(screen.getByText(useRecovery[locale])).not.toBeNull();
    });
  }

  it("shows a mapped error and moves focus to it", async () => {
    vi.mocked(api.answer).mockResolvedValueOnce({ type: "error", error: { code: "invalid_code" } });
    renderWithLocale(<Totp challenge={challenge} onChallenge={() => {}} />, "en");
    const clipboardData = { getData: () => "000000" };
    fireEvent.paste(screen.getByLabelText(codeLabels.en), { clipboardData });
    fireEvent.click(screen.getByText(submits.en));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toBe("That code is not correct.");
    await waitFor(() => expect(document.activeElement).toBe(alert));
  });
});
