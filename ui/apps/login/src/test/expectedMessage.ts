import { setupI18n } from "@lingui/core";
import { messages as ar } from "../locales/ar/messages.po";
import { messages as en } from "../locales/en/messages.po";
import { messages as fa } from "../locales/fa/messages.po";
import type { Locale } from "../i18n";

const catalogs: Record<Locale, Record<string, string>> = { en, fa, ar };

export function expectedMessage(locale: Locale, id: string): string {
  const instance = setupI18n({ locale, messages: { [locale]: catalogs[locale] } });
  return instance._(id);
}
