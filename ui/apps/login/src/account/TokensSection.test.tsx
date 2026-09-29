import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import { dirOf, locales } from "../i18n";
import { renderWithLocale } from "../test/renderWithLocale";
import { TokensSection } from "./TokensSection";

vi.mock("../api", () => ({
  listTokens: vi.fn(),
  createToken: vi.fn(),
  revokeToken: vi.fn(),
  ApiError: class ApiError extends Error {
    code: string;
    constructor(code: string) {
      super(code);
      this.code = code;
    }
  },
}));

const descriptionLabels = { en: "Description", fa: "توضیحات", ar: "الوصف" };
const creates = { en: "Create token", fa: "ساخت توکن", ar: "إنشاء رمز" };

describe("TokensSection", () => {
  beforeEach(() => {
    vi.mocked(api.listTokens).mockReset();
    vi.mocked(api.listTokens).mockResolvedValue([]);
    vi.mocked(api.createToken).mockReset();
    vi.mocked(api.createToken).mockResolvedValue({ id: "tok-1", token: "brk_secretvalue" });
  });

  for (const locale of locales) {
    it(`creates a token in ${locale}, shows it once with the CSRF token`, async () => {
      renderWithLocale(<TokensSection csrf="csrf-acct" onSessionExpired={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      fireEvent.change(screen.getByLabelText(descriptionLabels[locale]), { target: { value: "laptop" } });
      fireEvent.click(screen.getByText(creates[locale]));
      await waitFor(() => {
        expect(api.createToken).toHaveBeenCalledWith("laptop", [], null, "csrf-acct");
      });
      expect(await screen.findByText("brk_secretvalue")).not.toBeNull();
    });
  }

  it("sends the user to login when creating a token answers no_session", async () => {
    vi.mocked(api.createToken).mockRejectedValue(new api.ApiError("no_session"));
    const onSessionExpired = vi.fn();
    renderWithLocale(<TokensSection csrf="csrf-acct" onSessionExpired={onSessionExpired} />, "en");
    fireEvent.change(screen.getByLabelText(descriptionLabels.en), { target: { value: "laptop" } });
    fireEvent.click(screen.getByText(creates.en));
    await waitFor(() => {
      expect(onSessionExpired).toHaveBeenCalledTimes(1);
    });
  });

  it("shows an error instead of an empty list when loading tokens fails", async () => {
    vi.mocked(api.listTokens).mockRejectedValue(new Error("network down"));
    renderWithLocale(<TokensSection csrf="csrf-acct" onSessionExpired={() => {}} />, "en");
    expect(await screen.findByRole("alert")).not.toBeNull();
  });
});
