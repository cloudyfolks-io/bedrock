import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import fixtures from "../fixtures/challenges.json";
import { dirOf, locales } from "../i18n";
import { renderWithLocale } from "../test/renderWithLocale";
import type { Challenge } from "../types";
import { Recovery } from "./Recovery";

vi.mock("../api", async () => {
  const actual = await vi.importActual<typeof import("../api")>("../api");
  return { ...actual, answer: vi.fn() };
});

const challenge = fixtures.find((f) => f.name === "recovery")!.challenge as Challenge;
const titles = { en: "Enter a recovery code", fa: "یک کد بازیابی وارد کنید", ar: "أدخل رمز الاسترداد" };
const labels = { en: "Recovery code", fa: "کد بازیابی", ar: "رمز الاسترداد" };
const submits = { en: "Verify", fa: "تأیید", ar: "تحقق" };

describe("Recovery", () => {
  beforeEach(() => {
    vi.mocked(api.answer).mockReset();
    vi.mocked(api.answer).mockResolvedValue({ type: "done", redirect: "/x" });
  });

  for (const locale of locales) {
    it(`renders in ${locale} and submits with method recovery and the CSRF token`, async () => {
      renderWithLocale(<Recovery challenge={challenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(titles[locale])).not.toBeNull();
      fireEvent.change(screen.getByLabelText(labels[locale]), { target: { value: "a2b3c4d5e6" } });
      fireEvent.click(screen.getByText(submits[locale]));
      await waitFor(() => {
        expect(api.answer).toHaveBeenCalledWith({ type: "recovery", code: "a2b3c4d5e6", method: "recovery" }, challenge.csrf);
      });
    });
  }
});
