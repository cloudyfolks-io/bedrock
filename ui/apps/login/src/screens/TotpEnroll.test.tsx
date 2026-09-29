import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import fixtures from "../fixtures/challenges.json";
import { dirOf, locales } from "../i18n";
import { otpauthSVG } from "../qr";
import { expectedMessage } from "../test/expectedMessage";
import { renderWithLocale } from "../test/renderWithLocale";
import type { Challenge } from "../types";
import { TotpEnroll } from "./TotpEnroll";

vi.mock("../api", async () => {
  const actual = await vi.importActual<typeof import("../api")>("../api");
  return { ...actual, answer: vi.fn() };
});
vi.mock("../qr", () => ({ otpauthSVG: vi.fn() }));

const enrollChallenge = fixtures.find((f) => f.name === "totp-enroll")!.challenge as Challenge;
const codesChallenge: Challenge = { type: "totp-enroll", csrf: "csrf-codes", recoveryCodes: ["a2b3c4d5e6", "f7g8h9jkmn"] };

describe("TotpEnroll", () => {
  beforeEach(() => {
    vi.mocked(api.answer).mockReset();
    vi.mocked(otpauthSVG).mockReset();
    vi.mocked(otpauthSVG).mockResolvedValue("<svg></svg>");
  });

  for (const locale of locales) {
    it(`renders the QR phase in ${locale} and submits the code with the CSRF token`, async () => {
      vi.mocked(api.answer).mockResolvedValueOnce({ type: "totp-enroll", csrf: "csrf-codes", recoveryCodes: ["a2b3c4d5e6"] });
      renderWithLocale(<TotpEnroll challenge={enrollChallenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(expectedMessage(locale, "login.enroll.title"))).not.toBeNull();
      await waitFor(() => expect(otpauthSVG).toHaveBeenCalledWith(enrollChallenge.enroll!.otpauthURL));
      const codeLabel = `${expectedMessage(locale, "login.enroll.codeLabel")} 1`;
      const clipboardData = { getData: () => "654321" };
      fireEvent.paste(screen.getByLabelText(codeLabel), { clipboardData });
      fireEvent.click(screen.getByText(expectedMessage(locale, "login.enroll.submit")));
      await waitFor(() => {
        expect(api.answer).toHaveBeenCalledWith({ type: "totp-enroll", code: "654321" }, enrollChallenge.csrf);
      });
    });

    it(`renders the recovery-codes phase in ${locale} and continues with no code`, async () => {
      vi.mocked(api.answer).mockResolvedValueOnce({ type: "done", redirect: "/x" });
      renderWithLocale(<TotpEnroll challenge={codesChallenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(expectedMessage(locale, "login.enroll.recoveryCodes"))).not.toBeNull();
      expect(screen.getByText("a2b3c4d5e6")).not.toBeNull();
      fireEvent.click(screen.getByText(expectedMessage(locale, "login.enroll.continue")));
      await waitFor(() => {
        expect(api.answer).toHaveBeenCalledWith({ type: "totp-enroll" }, "csrf-codes");
      });
    });
  }
});
