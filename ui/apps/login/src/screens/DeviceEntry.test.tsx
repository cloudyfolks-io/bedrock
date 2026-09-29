import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import { dirOf, locales } from "../i18n";
import { expectedMessage } from "../test/expectedMessage";
import { renderWithLocale } from "../test/renderWithLocale";
import { DeviceEntry } from "./DeviceEntry";

vi.mock("../api", async () => {
  const actual = await vi.importActual<typeof import("../api")>("../api");
  return { ...actual, startDevice: vi.fn() };
});

describe("DeviceEntry", () => {
  beforeEach(() => {
    vi.mocked(api.startDevice).mockReset();
    vi.mocked(api.startDevice).mockResolvedValue({ type: "username", csrf: "csrf-1" });
    window.history.pushState({}, "", "/login/device");
  });

  for (const locale of locales) {
    it(`renders in ${locale} and starts the device flow with the typed user code`, async () => {
      renderWithLocale(<DeviceEntry onChallenge={() => {}} />, locale);
      expect(document.documentElement.dir).toBe(dirOf(locale));
      expect(screen.getByText(expectedMessage(locale, "login.device.enterCode"))).not.toBeNull();
      fireEvent.change(screen.getByLabelText(expectedMessage(locale, "login.device.codeLabel")), { target: { value: "ABCD-EFGH" } });
      fireEvent.click(screen.getByText(expectedMessage(locale, "login.device.submit")));
      await waitFor(() => {
        expect(api.startDevice).toHaveBeenCalledWith("ABCD-EFGH");
      });
    });
  }

  it("prefills the user code from the user_code query parameter, normalized", () => {
    window.history.pushState({}, "", "/login/device?user_code=wxzb+cdfg");
    renderWithLocale(<DeviceEntry onChallenge={() => {}} />, "en");
    const field = screen.getByLabelText(expectedMessage("en", "login.device.codeLabel")) as HTMLInputElement;
    expect(field.value).toBe("WXZB-CDFG");
  });

  it("keeps the prefilled user code editable", async () => {
    window.history.pushState({}, "", "/login/device?user_code=WXZB-CDFG");
    renderWithLocale(<DeviceEntry onChallenge={() => {}} />, "en");
    const field = screen.getByLabelText(expectedMessage("en", "login.device.codeLabel"));
    fireEvent.change(field, { target: { value: "PQRS-TVWX" } });
    fireEvent.click(screen.getByText(expectedMessage("en", "login.device.submit")));
    await waitFor(() => {
      expect(api.startDevice).toHaveBeenCalledWith("PQRS-TVWX");
    });
  });

  it("leaves the user code empty when there is no user_code query parameter", () => {
    renderWithLocale(<DeviceEntry onChallenge={() => {}} />, "en");
    const field = screen.getByLabelText(expectedMessage("en", "login.device.codeLabel")) as HTMLInputElement;
    expect(field.value).toBe("");
  });
});
