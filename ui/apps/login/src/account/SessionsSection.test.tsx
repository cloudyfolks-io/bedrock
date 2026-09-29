import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import { dirOf, locales } from "../i18n";
import { renderWithLocale } from "../test/renderWithLocale";
import { SessionsSection } from "./SessionsSection";

vi.mock("../api", () => ({
  listSessions: vi.fn(),
  revokeSession: vi.fn(),
  logout: vi.fn(),
}));

const currents = { en: "This device", fa: "این دستگاه", ar: "هذا الجهاز" };
const logouts = { en: "Sign out", fa: "خروج از سیستم", ar: "تسجيل الخروج" };

describe("SessionsSection", () => {
  beforeEach(() => {
    vi.mocked(api.listSessions).mockReset();
    vi.mocked(api.listSessions).mockResolvedValue([
      { id: "sess-1", current: true, authTime: "2026-09-28T00:00:00Z", lastSeen: null, userAgent: "curl", clientIP: "127.0.0.1" },
    ]);
    vi.mocked(api.logout).mockReset();
    vi.mocked(api.logout).mockResolvedValue(undefined);
  });

  for (const locale of locales) {
    it(`marks the current session in ${locale} and logs out with the CSRF token`, async () => {
      renderWithLocale(<SessionsSection csrf="csrf-acct" />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(await screen.findByText(currents[locale])).not.toBeNull();
      fireEvent.click(screen.getByText(logouts[locale]));
      await waitFor(() => {
        expect(api.logout).toHaveBeenCalledWith("csrf-acct");
      });
    });
  }
});
