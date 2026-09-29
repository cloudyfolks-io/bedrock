import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import { dirOf, locales } from "../i18n";
import { otpauthSVG } from "../qr";
import { expectedMessage } from "../test/expectedMessage";
import { renderWithLocale } from "../test/renderWithLocale";
import { TotpSection } from "./TotpSection";

const { MockApiError } = vi.hoisted(() => {
  class MockApiError extends Error {
    code: string;
    constructor(code: string) {
      super(code);
      this.code = code;
    }
  }
  return { MockApiError };
});

vi.mock("../api", () => ({
  beginTOTP: vi.fn(),
  verifyTOTP: vi.fn(),
  removeTOTP: vi.fn(),
  ApiError: MockApiError,
}));
vi.mock("../qr", () => ({ otpauthSVG: vi.fn() }));

describe("TotpSection", () => {
  beforeEach(() => {
    vi.mocked(api.removeTOTP).mockReset();
  });

  for (const locale of locales) {
    it(`shows the removeBlocked message in ${locale} when the server refuses`, async () => {
      vi.mocked(api.removeTOTP).mockRejectedValue(new MockApiError("second_factor_required"));
      renderWithLocale(
        <TotpSection
          csrf="csrf-acct"
          methods={[{ method: "totp", enrolledAt: "2026-01-01T00:00:00Z", lastUsed: null }]}
          onChanged={() => {}}
          onSessionExpired={() => {}}
        />,
        locale,
      );
      expect(document.documentElement.dir).toBe(dirOf(locale));
      fireEvent.click(screen.getByText(expectedMessage(locale, "account.totp.remove")));
      expect(await screen.findByRole("alert")).toHaveProperty(
        "textContent",
        expectedMessage(locale, "account.totp.removeBlocked"),
      );
    });
  }
});
