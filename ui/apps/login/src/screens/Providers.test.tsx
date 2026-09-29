import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import fixtures from "../fixtures/challenges.json";
import { dirOf, locales } from "../i18n";
import { expectedMessage } from "../test/expectedMessage";
import { renderWithLocale } from "../test/renderWithLocale";
import type { Challenge } from "../types";
import { Providers } from "./Providers";

vi.mock("../api", async () => {
  const actual = await vi.importActual<typeof import("../api")>("../api");
  return { ...actual, answer: vi.fn() };
});

const challenge = fixtures.find((f) => f.name === "providers")!.challenge as Challenge;

describe("Providers", () => {
  beforeEach(() => {
    vi.mocked(api.answer).mockReset();
    vi.mocked(api.answer).mockResolvedValue({ type: "redirect", redirect: "/x" });
  });

  for (const locale of locales) {
    it(`renders in ${locale} and answers with the chosen provider and the CSRF token`, async () => {
      renderWithLocale(<Providers challenge={challenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(expectedMessage(locale, "login.providers.title"))).not.toBeNull();
      fireEvent.click(screen.getByText("Dadehat SSO"));
      await waitFor(() => {
        expect(api.answer).toHaveBeenCalledWith({ type: "providers", provider: "dex" }, challenge.csrf);
      });
    });
  }
});
