import { i18n } from "@lingui/core";
import { messages as en } from "./locales/en/messages.po";
import { messages as fa } from "./locales/fa/messages.po";
import { messages as ar } from "./locales/ar/messages.po";

export const locales = ["en", "fa", "ar"] as const;
export type Locale = (typeof locales)[number];

const catalogs: Record<Locale, Record<string, string>> = { en, fa, ar };
const rtlLocales: ReadonlySet<Locale> = new Set(["fa", "ar"]);
const storageKey = "bedrock.locale";

function isLocale(value: string): value is Locale {
  return (locales as readonly string[]).includes(value);
}

export function dirOf(locale: Locale): "ltr" | "rtl" {
  return rtlLocales.has(locale) ? "rtl" : "ltr";
}

export function pickLocale(stored: string | null, acceptLanguage: readonly string[]): Locale {
  if (stored && isLocale(stored)) {
    return stored;
  }
  for (const candidate of acceptLanguage) {
    const base = candidate.split("-")[0]?.toLowerCase() ?? "";
    if (isLocale(base)) {
      return base;
    }
  }
  return "en";
}

export function readStoredLocale(): string | null {
  try {
    return localStorage.getItem(storageKey);
  } catch {
    return null;
  }
}

export function activate(locale: Locale): void {
  i18n.load(locale, catalogs[locale]);
  i18n.activate(locale);
  document.documentElement.lang = locale;
  document.documentElement.dir = dirOf(locale);
  try {
    localStorage.setItem(storageKey, locale);
  } catch {}
}
