import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import fixtures from "../fixtures/challenges.json";
import { dirOf, locales } from "../i18n";
import { otpauthSVG } from "../qr";
import { renderWithLocale } from "../test/renderWithLocale";
import type { Challenge } from "../types";
import { TotpEnroll } from "./TotpEnroll";

vi.mock("../api", () => ({ answer: vi.fn() }));
vi.mock("../qr", () => ({ otpauthSVG: vi.fn() }));

const enrollChallenge = fixtures.find((f) => f.name === "totp-enroll")!.challenge as Challenge;
const codesChallenge: Challenge = { type: "totp-enroll", csrf: "csrf-codes", recoveryCodes: ["a2b3c4d5e6", "f7g8h9jkmn"] };

const enrollTitles = { en: "Set up your authenticator", fa: "برنامه احراز هویت خود را تنظیم کنید", ar: "إعداد تطبيق المصادقة" };
const codeLabels = { en: "Enter the 6-digit code 1", fa: "کد ۶ رقمی را وارد کنید 1", ar: "أدخل الرمز المكوّن من 6 أرقام 1" };
const submits = { en: "Verify", fa: "تأیید", ar: "تحقق" };
const recoveryTitles = { en: "Save these recovery codes", fa: "این کدهای بازیابی را ذخیره کنید", ar: "احفظ رموز الاسترداد هذه" };
const continues = { en: "Continue", fa: "ادامه", ar: "متابعة" };

describe("TotpEnroll", () => {
  beforeEach(() => {
    vi.mocked(api.answer).mockReset();
    vi.mocked(otpauthSVG).mockReset();
    vi.mocked(otpauthSVG).mockResolvedValue("<svg></svg>");
  });

  for (const locale of locales) {
    it(`renders the QR phase in ${locale} and submits the code with the CSRF token`, async () => {
      vi.mocked(api.answer).mockResolvedValueOnce({ type: "totp-enroll", csrf: "csrf-codes", recoveryCodes: ["a2b3c4d5e6"] });
      renderWithLocale(<TotpEnroll challenge={enrollChallenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(enrollTitles[locale])).not.toBeNull();
      await waitFor(() => expect(otpauthSVG).toHaveBeenCalledWith(enrollChallenge.enroll!.otpauthURL));
      const clipboardData = { getData: () => "654321" };
      fireEvent.paste(screen.getByLabelText(codeLabels[locale]), { clipboardData });
      fireEvent.click(screen.getByText(submits[locale]));
      await waitFor(() => {
        expect(api.answer).toHaveBeenCalledWith({ type: "totp-enroll", code: "654321" }, enrollChallenge.csrf);
      });
    });

    it(`renders the recovery-codes phase in ${locale} and continues with no code`, async () => {
      vi.mocked(api.answer).mockResolvedValueOnce({ type: "done", redirect: "/x" });
      renderWithLocale(<TotpEnroll challenge={codesChallenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(recoveryTitles[locale])).not.toBeNull();
      expect(screen.getByText("a2b3c4d5e6")).not.toBeNull();
      fireEvent.click(screen.getByText(continues[locale]));
      await waitFor(() => {
        expect(api.answer).toHaveBeenCalledWith({ type: "totp-enroll" }, "csrf-codes");
      });
    });
  }
});
