import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import { dirOf, locales } from "../i18n";
import { expectedMessage } from "../test/expectedMessage";
import { renderWithLocale } from "../test/renderWithLocale";
import { RecoverySection } from "./RecoverySection";

vi.mock("../api", () => ({ newRecoveryCodes: vi.fn() }));

describe("RecoverySection", () => {
  beforeEach(() => {
    vi.mocked(api.newRecoveryCodes).mockReset();
    vi.mocked(api.newRecoveryCodes).mockResolvedValue({ recoveryCodes: ["a2b3c4d5e6"] });
  });

  for (const locale of locales) {
    it(`shows the codes once in ${locale} then dismisses them`, async () => {
      renderWithLocale(<RecoverySection csrf="csrf-acct" onSessionExpired={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      fireEvent.click(screen.getByText(expectedMessage(locale, "account.recovery.generate")));
      expect(await screen.findByText("a2b3c4d5e6")).not.toBeNull();
      fireEvent.click(screen.getByText(expectedMessage(locale, "account.done")));
      expect(screen.queryByText("a2b3c4d5e6")).toBeNull();
    });
  }
});
