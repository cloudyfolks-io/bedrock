import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import fixtures from "../fixtures/challenges.json";
import { dirOf, locales } from "../i18n";
import { expectedMessage } from "../test/expectedMessage";
import { renderWithLocale } from "../test/renderWithLocale";
import type { Challenge } from "../types";
import { Totp } from "./Totp";

vi.mock("../api", async () => {
  const actual = await vi.importActual<typeof import("../api")>("../api");
  return { ...actual, answer: vi.fn() };
});

const challenge = fixtures.find((f) => f.name === "totp")!.challenge as Challenge;

describe("Totp", () => {
  beforeEach(() => {
    vi.mocked(api.answer).mockReset();
    vi.mocked(api.answer).mockResolvedValue({ type: "done", redirect: "/x" });
  });

  for (const locale of locales) {
    it(`renders in ${locale}, focuses the first digit and submits six typed digits with the CSRF token`, async () => {
      renderWithLocale(<Totp challenge={challenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(expectedMessage(locale, "login.totp.title"))).not.toBeNull();
      const codeLabel = `${expectedMessage(locale, "login.totp.label")} 1`;
      expect(document.activeElement).toBe(screen.getByLabelText(codeLabel));
      const clipboardData = { getData: () => "123456" };
      fireEvent.paste(screen.getByLabelText(codeLabel), { clipboardData });
      fireEvent.click(screen.getByText(expectedMessage(locale, "login.totp.submit")));
      await waitFor(() => {
        expect(api.answer).toHaveBeenCalledWith({ type: "totp", code: "123456" }, challenge.csrf);
      });
      expect(screen.getByText(expectedMessage(locale, "login.totp.useRecovery"))).not.toBeNull();
    });
  }

  it("shows a mapped error and moves focus to it", async () => {
    vi.mocked(api.answer).mockResolvedValueOnce({ type: "error", error: { code: "invalid_code" } });
    renderWithLocale(<Totp challenge={challenge} onChallenge={() => {}} />, "en");
    const codeLabel = `${expectedMessage("en", "login.totp.label")} 1`;
    const clipboardData = { getData: () => "000000" };
    fireEvent.paste(screen.getByLabelText(codeLabel), { clipboardData });
    fireEvent.click(screen.getByText(expectedMessage("en", "login.totp.submit")));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toBe(expectedMessage("en", "login.error.invalid_code"));
    await waitFor(() => expect(document.activeElement).toBe(alert));
  });

  it("maps a rate-limited rejection to the existing rate-limit message", async () => {
    vi.mocked(api.answer).mockRejectedValueOnce(new api.ApiError("rate_limited"));
    renderWithLocale(<Totp challenge={challenge} onChallenge={() => {}} />, "en");
    const codeLabel = `${expectedMessage("en", "login.totp.label")} 1`;
    const clipboardData = { getData: () => "000000" };
    fireEvent.paste(screen.getByLabelText(codeLabel), { clipboardData });
    fireEvent.click(screen.getByText(expectedMessage("en", "login.totp.submit")));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toBe(expectedMessage("en", "login.error.rate_limited"));
  });

  it("relabels the challenge as recovery locally without calling the API", () => {
    const onChallenge = vi.fn();
    renderWithLocale(<Totp challenge={challenge} onChallenge={onChallenge} />, "en");
    fireEvent.click(screen.getByText(expectedMessage("en", "login.totp.useRecovery")));
    expect(onChallenge).toHaveBeenCalledWith({ ...challenge, type: "recovery" });
    expect(api.answer).not.toHaveBeenCalled();
  });
});
