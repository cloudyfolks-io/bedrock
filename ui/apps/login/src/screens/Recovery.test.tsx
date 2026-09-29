import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import fixtures from "../fixtures/challenges.json";
import { dirOf, locales } from "../i18n";
import { expectedMessage } from "../test/expectedMessage";
import { renderWithLocale } from "../test/renderWithLocale";
import type { Challenge } from "../types";
import { Recovery } from "./Recovery";

vi.mock("../api", async () => {
  const actual = await vi.importActual<typeof import("../api")>("../api");
  return { ...actual, answer: vi.fn() };
});

const challenge = fixtures.find((f) => f.name === "recovery")!.challenge as Challenge;

describe("Recovery", () => {
  beforeEach(() => {
    vi.mocked(api.answer).mockReset();
    vi.mocked(api.answer).mockResolvedValue({ type: "done", redirect: "/x" });
  });

  for (const locale of locales) {
    it(`renders in ${locale} and submits with method recovery and the CSRF token`, async () => {
      renderWithLocale(<Recovery challenge={challenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(expectedMessage(locale, "login.recovery.title"))).not.toBeNull();
      fireEvent.change(screen.getByLabelText(expectedMessage(locale, "login.recovery.label")), { target: { value: "a2b3c4d5e6" } });
      fireEvent.click(screen.getByText(expectedMessage(locale, "login.recovery.submit")));
      await waitFor(() => {
        expect(api.answer).toHaveBeenCalledWith({ type: "recovery", code: "a2b3c4d5e6", method: "recovery" }, challenge.csrf);
      });
    });
  }
});
