import { describe, expect, it } from "vitest";
import { formatBytes, formatDateTime, formatNumber, formatQuantity, formatRelative } from "./format";

describe("formatBytes", () => {
  it("formats binary units", () => {
    expect(formatBytes(1536, "en")).toBe("1.5 KiB");
    expect(formatBytes(0, "en")).toBe("0 B");
    expect(formatBytes(1024 ** 3, "en")).toBe("1 GiB");
  });

  it("uses Persian digits and the unit for fa", () => {
    const text = formatBytes(5 * 1024 ** 3, "fa");
    expect(text).toContain("۵");
    expect(text).toContain("GiB");
  });
});

describe("formatNumber", () => {
  it("uses the digits of the locale", () => {
    expect(formatNumber(1234, "en")).toBe("1,234");
    expect(formatNumber(12, "fa")).toBe("۱۲");
    expect(formatNumber(12, "ar")).toBe("١٢");
  });
});

describe("formatRelative", () => {
  const now = new Date("2026-10-08T12:00:00Z");

  it("says minutes ago in English", () => {
    expect(formatRelative("2026-10-08T11:58:00Z", now, "en")).toBe("2 minutes ago");
  });

  it("localizes for Arabic", () => {
    const text = formatRelative("2026-10-08T11:58:00Z", now, "ar");
    expect(text).not.toBe("2 minutes ago");
    expect(text.length).toBeGreaterThan(0);
  });

  it("picks days for long gaps", () => {
    expect(formatRelative("2026-10-05T12:00:00Z", now, "en")).toBe("3 days ago");
  });
});

describe("formatDateTime", () => {
  it("keeps the Gregorian calendar", () => {
    expect(formatDateTime("2026-10-08T12:00:00Z", "en")).toContain("2026");
    expect(formatDateTime("2026-10-08T12:00:00Z", "fa")).toContain("۲۰۲۶");
  });
});

describe("formatQuantity", () => {
  it("converts Kubernetes quantities", () => {
    expect(formatQuantity("8Gi")).toBe("8 GiB");
    expect(formatQuantity("500m")).toBe("0.5");
    expect(formatQuantity("4")).toBe("4");
  });

  it("keeps text it cannot read", () => {
    expect(formatQuantity("lots")).toBe("lots");
  });
});
