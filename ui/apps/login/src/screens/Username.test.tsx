import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import fixtures from "../fixtures/challenges.json";
import { dirOf, locales } from "../i18n";
import { renderWithLocale } from "../test/renderWithLocale";
import type { Challenge } from "../types";
import { Username } from "./Username";

vi.mock("../api", async () => {
  const actual = await vi.importActual<typeof import("../api")>("../api");
  return { ...actual, answer: vi.fn() };
});

const challenge = fixtures.find((f) => f.name === "username")!.challenge as Challenge;
const titles = { en: "Sign in", fa: "ورود", ar: "تسجيل الدخول" };
const labels = { en: "Username", fa: "نام کاربری", ar: "اسم المستخدم" };
const buttons = { en: "Continue", fa: "ادامه", ar: "متابعة" };

describe("Username", () => {
  beforeEach(() => {
    vi.mocked(api.answer).mockReset();
    vi.mocked(api.answer).mockResolvedValue({ type: "password", csrf: "csrf-2", username: "alice" });
  });

  for (const locale of locales) {
    it(`renders in ${locale} and submits with the CSRF token`, async () => {
      renderWithLocale(<Username challenge={challenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(titles[locale])).not.toBeNull();
      fireEvent.change(screen.getByLabelText(labels[locale]), { target: { value: "alice" } });
      fireEvent.click(screen.getByText(buttons[locale]));
      await waitFor(() => {
        expect(api.answer).toHaveBeenCalledWith({ type: "username", username: "alice" }, challenge.csrf);
      });
    });
  }

  it("offers the providers listed on the username challenge", async () => {
    const withProviders: Challenge = { ...challenge, providers: [{ name: "dex", displayName: "Dadehat SSO", type: "oidc" }] };
    renderWithLocale(<Username challenge={withProviders} onChallenge={() => {}} />, "en");
    fireEvent.click(screen.getByText("Dadehat SSO"));
    await waitFor(() => {
      expect(api.answer).toHaveBeenCalledWith({ type: "providers", provider: "dex" }, challenge.csrf);
    });
  });
});
