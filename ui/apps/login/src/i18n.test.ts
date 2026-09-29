import { afterEach, describe, expect, it, vi } from "vitest";
import { activate, dirOf, pickLocale } from "./i18n";

describe("dirOf", () => {
  it("is rtl for fa and ar, ltr for en", () => {
    expect(dirOf("fa")).toBe("rtl");
    expect(dirOf("ar")).toBe("rtl");
    expect(dirOf("en")).toBe("ltr");
  });
});

describe("pickLocale", () => {
  it("prefers the stored locale", () => {
    expect(pickLocale("fa", ["en-US"])).toBe("fa");
  });

  it("falls back to the first matching Accept-Language subtag", () => {
    expect(pickLocale(null, ["de-DE", "ar-EG"])).toBe("ar");
  });

  it("falls back to en", () => {
    expect(pickLocale(null, ["de-DE"])).toBe("en");
  });
});

describe("activate", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("sets lang and dir and tolerates storage failure", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(() => activate("ar")).not.toThrow();
    expect(document.documentElement.lang).toBe("ar");
    expect(document.documentElement.dir).toBe("rtl");
  });
});
