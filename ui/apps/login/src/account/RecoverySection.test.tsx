import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import { dirOf, locales } from "../i18n";
import { renderWithLocale } from "../test/renderWithLocale";
import { RecoverySection } from "./RecoverySection";

vi.mock("../api", () => ({ newRecoveryCodes: vi.fn() }));

const generates = { en: "Generate new recovery codes", fa: "ساخت کدهای بازیابی جدید", ar: "إنشاء رموز استرداد جديدة" };
const dones = { en: "Done", fa: "پایان", ar: "تم" };

describe("RecoverySection", () => {
  beforeEach(() => {
    vi.mocked(api.newRecoveryCodes).mockReset();
    vi.mocked(api.newRecoveryCodes).mockResolvedValue({ recoveryCodes: ["a2b3c4d5e6"] });
  });

  for (const locale of locales) {
    it(`shows the codes once in ${locale} then dismisses them`, async () => {
      renderWithLocale(<RecoverySection csrf="csrf-acct" onSessionExpired={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      fireEvent.click(screen.getByText(generates[locale]));
      expect(await screen.findByText("a2b3c4d5e6")).not.toBeNull();
      fireEvent.click(screen.getByText(dones[locale]));
      expect(screen.queryByText("a2b3c4d5e6")).toBeNull();
    });
  }
});
