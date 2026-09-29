import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import fixtures from "../fixtures/challenges.json";
import { dirOf, locales } from "../i18n";
import { renderWithLocale } from "../test/renderWithLocale";
import type { Challenge } from "../types";
import { Password } from "./Password";

vi.mock("../api", () => ({ answer: vi.fn() }));

const challenge = fixtures.find((f) => f.name === "password")!.challenge as Challenge;
const titles = { en: "Enter your password", fa: "رمز عبور خود را وارد کنید", ar: "أدخل كلمة المرور الخاصة بك" };
const labels = { en: "Password", fa: "رمز عبور", ar: "كلمة المرور" };
const submits = { en: "Sign in", fa: "ورود", ar: "تسجيل الدخول" };
const invalidCredentials = {
  en: "That username or password is not correct.",
  fa: "نام کاربری یا رمز عبور نادرست است.",
  ar: "اسم المستخدم أو كلمة المرور غير صحيحة.",
};

describe("Password", () => {
  beforeEach(() => {
    vi.mocked(api.answer).mockReset();
  });

  for (const locale of locales) {
    it(`renders in ${locale}, submits with the CSRF token and shows a mapped error`, async () => {
      vi.mocked(api.answer).mockResolvedValueOnce({ type: "error", error: { code: "invalid_credentials" } });
      renderWithLocale(<Password challenge={challenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(titles[locale])).not.toBeNull();
      fireEvent.change(screen.getByLabelText(labels[locale]), { target: { value: "hunter2" } });
      fireEvent.click(screen.getByText(submits[locale]));
      await waitFor(() => {
        expect(api.answer).toHaveBeenCalledWith({ type: "password", password: "hunter2" }, challenge.csrf);
      });
      expect(await screen.findByRole("alert")).toHaveProperty("textContent", invalidCredentials[locale]);
    });
  }
});
