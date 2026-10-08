import type { Locale } from "./i18n";

const numberingLocales: Record<Locale, string> = {
  en: "en",
  fa: "fa-u-nu-arabext",
  ar: "ar-u-nu-arab",
};

const binaryUnits = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"] as const;

export function formatNumber(value: number, locale: Locale): string {
  return new Intl.NumberFormat(numberingLocales[locale], { maximumFractionDigits: 2 }).format(value);
}

export function formatBytes(bytes: number, locale: Locale): string {
  const exponent = bytes <= 0 ? 0 : Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), binaryUnits.length - 1);
  const scaled = bytes / 1024 ** exponent;
  const number = new Intl.NumberFormat(numberingLocales[locale], { maximumFractionDigits: 1 }).format(scaled);
  return `${number} ${binaryUnits[exponent]}`;
}

export function formatDateTime(iso: string, locale: Locale): string {
  const tag = numberingLocales[locale].includes("-u-") ? `${numberingLocales[locale]}-ca-gregory` : `${numberingLocales[locale]}-u-ca-gregory`;
  return new Intl.DateTimeFormat(tag, { dateStyle: "medium", timeStyle: "short", timeZone: "UTC" }).format(new Date(iso));
}

const relativeSteps: { unit: Intl.RelativeTimeFormatUnit; seconds: number }[] = [
  { unit: "day", seconds: 86400 },
  { unit: "hour", seconds: 3600 },
  { unit: "minute", seconds: 60 },
  { unit: "second", seconds: 1 },
];

export function formatRelative(iso: string, now: Date, locale: Locale): string {
  const diffSeconds = Math.round((new Date(iso).getTime() - now.getTime()) / 1000);
  const step = relativeSteps.find((candidate) => Math.abs(diffSeconds) >= candidate.seconds) ?? relativeSteps[relativeSteps.length - 1];
  const formatter = new Intl.RelativeTimeFormat(numberingLocales[locale], { numeric: "auto" });
  return formatter.format(Math.round(diffSeconds / (step?.seconds ?? 1)), step?.unit ?? "second");
}

const quantityPattern = /^(\d+(?:\.\d+)?)(Ki|Mi|Gi|Ti|Pi|k|M|G|T|P|m)?$/;

export function formatQuantity(text: string): string {
  const match = quantityPattern.exec(text);
  if (!match?.[1]) {
    return text;
  }
  const suffix = match[2];
  if (!suffix) {
    return match[1];
  }
  if (suffix === "m") {
    return String(Number(match[1]) / 1000);
  }
  return `${match[1]} ${suffix}B`;
}

const quantityUnits: Record<string, number> = {
  Ki: 1024,
  Mi: 1024 ** 2,
  Gi: 1024 ** 3,
  Ti: 1024 ** 4,
  Pi: 1024 ** 5,
  k: 1e3,
  M: 1e6,
  G: 1e9,
  T: 1e12,
  P: 1e15,
  m: 1e-3,
};

export function parseQuantity(text: string): number {
  const match = quantityPattern.exec(text);
  if (!match?.[1]) {
    return Number.NaN;
  }
  return Number(match[1]) * (match[2] ? (quantityUnits[match[2]] ?? 1) : 1);
}

export type Unit = "bytes" | "bytesPerSecond" | "cores" | "ratio";

export function formatUnit(value: number, unit: Unit, locale: Locale): string {
  switch (unit) {
    case "bytes":
      return formatBytes(value, locale);
    case "bytesPerSecond":
      return `${formatBytes(value, locale)}/s`;
    case "cores":
      return new Intl.NumberFormat(numberingLocales[locale], { maximumFractionDigits: 2 }).format(value);
    case "ratio":
      return new Intl.NumberFormat(numberingLocales[locale], { style: "percent", maximumFractionDigits: 0 }).format(value);
  }
}
