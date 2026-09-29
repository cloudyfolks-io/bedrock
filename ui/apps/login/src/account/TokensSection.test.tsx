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
      renderWithLocale(<TokensSection csrf="csrf-acct" />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      fireEvent.change(screen.getByLabelText(descriptionLabels[locale]), { target: { value: "laptop" } });
      fireEvent.click(screen.getByText(creates[locale]));
      await waitFor(() => {
        expect(api.createToken).toHaveBeenCalledWith("laptop", [], null, "csrf-acct");
      });
      expect(await screen.findByText("brk_secretvalue")).not.toBeNull();
    });
  }
});
