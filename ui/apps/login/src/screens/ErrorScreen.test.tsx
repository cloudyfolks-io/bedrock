import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import fixtures from "../fixtures/challenges.json";
import { dirOf, locales } from "../i18n";
import { renderWithLocale } from "../test/renderWithLocale";
import type { Challenge } from "../types";
import { ErrorScreen } from "./ErrorScreen";

const challenge = fixtures.find((f) => f.name === "error")!.challenge as Challenge;
const messages = {
  en: "That username or password is not correct.",
  fa: "نام کاربری یا رمز عبور نادرست است.",
  ar: "اسم المستخدم أو كلمة المرور غير صحيحة.",
};

describe("ErrorScreen", () => {
  for (const locale of locales) {
    it(`renders the mapped message in ${locale}, never the server code`, () => {
      renderWithLocale(<ErrorScreen challenge={challenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByRole("alert").textContent).toBe(messages[locale]);
      expect(screen.queryByText("invalid_credentials")).toBeNull();
    });
  }
});
