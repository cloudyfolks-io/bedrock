import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import fixtures from "../fixtures/challenges.json";
import { dirOf, locales } from "../i18n";
import { renderWithLocale } from "../test/renderWithLocale";
import type { Challenge } from "../types";
import { Done } from "./Done";

const challenge = fixtures.find((f) => f.name === "done")!.challenge as Challenge;
const titles = { en: "Signed in. Redirecting…", fa: "ورود انجام شد. در حال هدایت…", ar: "تم تسجيل الدخول. جارٍ إعادة التوجيه…" };

describe("Done", () => {
  for (const locale of locales) {
    it(`renders in ${locale}`, () => {
      renderWithLocale(<Done challenge={challenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(titles[locale])).not.toBeNull();
    });
  }
});
