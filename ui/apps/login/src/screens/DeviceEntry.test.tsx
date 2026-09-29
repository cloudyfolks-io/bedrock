import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import { dirOf, locales } from "../i18n";
import { renderWithLocale } from "../test/renderWithLocale";
import { DeviceEntry } from "./DeviceEntry";

vi.mock("../api", () => ({ startDevice: vi.fn() }));

const titles = { en: "Enter the code shown on your device", fa: "کدی را که روی دستگاه شما نشان داده شده وارد کنید", ar: "أدخل الرمز الظاهر على جهازك" };
const labels = { en: "Device code", fa: "کد دستگاه", ar: "رمز الجهاز" };
const submits = { en: "Continue", fa: "ادامه", ar: "متابعة" };

describe("DeviceEntry", () => {
  beforeEach(() => {
    vi.mocked(api.startDevice).mockReset();
    vi.mocked(api.startDevice).mockResolvedValue({ type: "username", csrf: "csrf-1" });
  });

  for (const locale of locales) {
    it(`renders in ${locale} and starts the device flow with the typed user code`, async () => {
      renderWithLocale(<DeviceEntry onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(titles[locale])).not.toBeNull();
      fireEvent.change(screen.getByLabelText(labels[locale]), { target: { value: "ABCD-EFGH" } });
      fireEvent.click(screen.getByText(submits[locale]));
      await waitFor(() => {
        expect(api.startDevice).toHaveBeenCalledWith("ABCD-EFGH");
      });
    });
  }
});
