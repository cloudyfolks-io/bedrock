import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import fixtures from "../fixtures/challenges.json";
import { dirOf, locales } from "../i18n";
import { expectedMessage } from "../test/expectedMessage";
import { renderWithLocale } from "../test/renderWithLocale";
import type { Challenge } from "../types";
import { Done } from "./Done";

const challenge = fixtures.find((f) => f.name === "done")!.challenge as Challenge;

describe("Done", () => {
  for (const locale of locales) {
    it(`renders in ${locale}`, () => {
      renderWithLocale(<Done challenge={challenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(expectedMessage(locale, "login.done"))).not.toBeNull();
    });
  }
});
