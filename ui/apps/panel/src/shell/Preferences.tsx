import { useLingui } from "@lingui/react/macro";
import { Select } from "@bedrock/design";
import { isLocale, locales, type Locale } from "../i18n";
import { useLocale } from "../LocaleProvider";

function languageLabel(locale: Locale): string {
  return new Intl.DisplayNames([locale], { type: "language" }).of(locale) ?? locale;
}

export function LanguageSelect() {
  const { t } = useLingui();
  const { locale, setLocale } = useLocale();
  return (
    <Select
      label={t({ id: "shell.language.label", message: "Language" })}
      value={locale}
      options={locales.map((value) => ({ value, label: languageLabel(value) }))}
      onValueChange={(value) => {
        if (isLocale(value)) {
          setLocale(value);
        }
      }}
    />
  );
}
