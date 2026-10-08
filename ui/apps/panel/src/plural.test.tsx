import { describe, expect, it } from "vitest";
import { setupI18n } from "@lingui/core";
import { messages as ar } from "./locales/ar/messages.po";
import { messages as en } from "./locales/en/messages.po";
import { messages as fa } from "./locales/fa/messages.po";

const catalogs = { en, fa, ar };
const counts = [0, 1, 2, 3, 11, 100];

function render(locale: "en" | "fa" | "ar", count: number): string {
  const instance = setupI18n({ locale, messages: { [locale]: catalogs[locale] } });
  return instance._("common.count.vms", { count });
}

describe("plural forms", () => {
  it("TestPluralForms English has one and other", () => {
    expect(render("en", 1)).toBe("1 virtual machine");
    expect(render("en", 2)).toBe("2 virtual machines");
    expect(render("en", 0)).toBe("0 virtual machines");
  });

  it("Persian has one and other and uses Persian digits", () => {
    expect(render("fa", 11)).toContain("۱۱");
    expect(render("fa", 1)).toContain("۱");
  });

  it("Arabic gives six different strings for the six forms", () => {
    const forms = counts.map((count) => render("ar", count));
    expect(new Set(forms).size).toBe(6);
  });
});
