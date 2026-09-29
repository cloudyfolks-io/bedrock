import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import fixtures from "../fixtures/challenges.json";
import { dirOf, locales } from "../i18n";
import { expectedMessage } from "../test/expectedMessage";
import { renderWithLocale } from "../test/renderWithLocale";
import type { Challenge } from "../types";
import { Password } from "./Password";

vi.mock("../api", async () => {
  const actual = await vi.importActual<typeof import("../api")>("../api");
  return { ...actual, answer: vi.fn() };
});

const challenge = fixtures.find((f) => f.name === "password")!.challenge as Challenge;

describe("Password", () => {
  beforeEach(() => {
    vi.mocked(api.answer).mockReset();
  });

  for (const locale of locales) {
    it(`renders in ${locale}, focuses the field, submits with the CSRF token and shows a mapped error`, async () => {
      vi.mocked(api.answer).mockResolvedValueOnce({ type: "error", error: { code: "invalid_credentials" } });
      renderWithLocale(<Password challenge={challenge} onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(expectedMessage(locale, "login.password.title"))).not.toBeNull();
      const field = screen.getByLabelText(expectedMessage(locale, "login.password.label"));
      expect(document.activeElement).toBe(field);
      fireEvent.change(field, { target: { value: "hunter2" } });
      fireEvent.click(screen.getByText(expectedMessage(locale, "login.password.submit")));
      await waitFor(() => {
        expect(api.answer).toHaveBeenCalledWith({ type: "password", password: "hunter2" }, challenge.csrf);
      });
      const alert = await screen.findByRole("alert");
      expect(alert).toHaveProperty("textContent", expectedMessage(locale, "login.error.invalid_credentials"));
      await waitFor(() => expect(document.activeElement).toBe(alert));
    });
  }

  it("submits successfully and hands the next challenge to onChallenge", async () => {
    const onChallenge = vi.fn();
    vi.mocked(api.answer).mockResolvedValueOnce({ type: "totp", csrf: "csrf-3", username: "alice" });
    renderWithLocale(<Password challenge={challenge} onChallenge={onChallenge} />, "en");
    fireEvent.change(screen.getByLabelText(expectedMessage("en", "login.password.label")), { target: { value: "hunter2" } });
    fireEvent.click(screen.getByText(expectedMessage("en", "login.password.submit")));
    await waitFor(() => {
      expect(onChallenge).toHaveBeenCalledWith({ type: "totp", csrf: "csrf-3", username: "alice" });
    });
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("maps a rate-limited rejection to the existing rate-limit message", async () => {
    vi.mocked(api.answer).mockRejectedValueOnce(new api.ApiError("rate_limited"));
    renderWithLocale(<Password challenge={challenge} onChallenge={() => {}} />, "en");
    fireEvent.change(screen.getByLabelText(expectedMessage("en", "login.password.label")), { target: { value: "hunter2" } });
    fireEvent.click(screen.getByText(expectedMessage("en", "login.password.submit")));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toBe(expectedMessage("en", "login.error.rate_limited"));
  });
});
