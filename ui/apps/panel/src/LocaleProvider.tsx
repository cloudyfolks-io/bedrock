import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import { i18n } from "@lingui/core";
import { I18nProvider } from "@lingui/react";
import { DirectionProvider } from "@base-ui/react/direction-provider";
import { activate, dirOf, pickLocale, type Locale } from "./i18n";
import { readStored } from "./storage";

interface LocaleValue {
  locale: Locale;
  dir: "ltr" | "rtl";
  setLocale: (locale: Locale) => void;
}

const LocaleContext = createContext<LocaleValue | null>(null);

function initialLocale(requested: Locale | undefined): Locale {
  const locale = requested ?? pickLocale(readStored("locale"), navigator.languages);
  activate(locale);
  return locale;
}

export function LocaleProvider({ initial, children }: { initial?: Locale; children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(() => initialLocale(initial));
  const setLocale = useCallback((next: Locale) => {
    activate(next);
    setLocaleState(next);
  }, []);
  const value = useMemo(() => ({ locale, dir: dirOf(locale), setLocale }), [locale, setLocale]);
  return (
    <LocaleContext.Provider value={value}>
      <I18nProvider i18n={i18n}>
        <DirectionProvider direction={value.dir}>{children}</DirectionProvider>
      </I18nProvider>
    </LocaleContext.Provider>
  );
}

export function useLocale(): LocaleValue {
  const value = useContext(LocaleContext);
  if (!value) {
    throw new Error("useLocale needs a LocaleProvider");
  }
  return value;
}
