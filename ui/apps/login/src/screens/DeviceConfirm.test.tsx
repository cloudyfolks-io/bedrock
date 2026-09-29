import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import fixtures from "../fixtures/challenges.json";
import { dirOf, locales } from "../i18n";
import { renderWithLocale } from "../test/renderWithLocale";
import type { Challenge } from "../types";
import { DeviceConfirm } from "./DeviceConfirm";

vi.mock("../api", () => ({ answer: vi.fn() }));

const challenge = fixtures.find((f) => f.name === "device-confirm")!.challenge as Challenge;
const titles = { en: "Confirm sign-in for this device?", fa: "ورود برای این دستگاه تأیید شود؟", ar: "هل تريد تأكيد تسجيل الدخول لهذا الجهاز؟" };
const approves = { en: "Approve", fa: "تأیید", ar: "موافقة" };
const denies = { en: "Deny", fa: "رد", ar: "رفض" };

describe("DeviceConfirm", () => {
  beforeEach(() => {
    vi.mocked(api.answer).mockReset();
    vi.mocked(api.answer).mockResolvedValue({ type: "done", redirect: "/x" });
  });

  for (const locale of locales) {
    it(`renders in ${locale} and approves with the CSRF token`, async () => {
      renderWithLocale(<DeviceConfirm challenge={challenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(titles[locale])).not.toBeNull();
      expect(screen.getByText("ABCD-EFGH")).not.toBeNull();
      fireEvent.click(screen.getByText(approves[locale]));
      await waitFor(() => {
        expect(api.answer).toHaveBeenCalledWith({ type: "device-confirm", approve: true }, challenge.csrf);
      });
    });

    it(`denies in ${locale} with the CSRF token`, async () => {
      renderWithLocale(<DeviceConfirm challenge={challenge} onChallenge={() => {}} />, locale);
      fireEvent.click(screen.getByText(denies[locale]));
      await waitFor(() => {
        expect(api.answer).toHaveBeenCalledWith({ type: "device-confirm", approve: false }, challenge.csrf);
      });
    });
  }
});
