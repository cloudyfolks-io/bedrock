import { useLingui } from "@lingui/react/macro";
import { Select } from "@bedrock/design";
import { isLocale, locales, type Locale } from "../i18n";
import { useLocale } from "../LocaleProvider";
import { useThemeChoice, type ThemeChoice } from "../theme";

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

export function ThemeSelect() {
  const { t } = useLingui();
  const { choice, setChoice } = useThemeChoice();
  return (
    <Select
      label={t({ id: "shell.theme.label", message: "Theme" })}
      value={choice}
      options={[
        { value: "system", label: t({ id: "shell.theme.system", message: "System" }) },
        { value: "light", label: t({ id: "shell.theme.light", message: "Light" }) },
        { value: "dark", label: t({ id: "shell.theme.dark", message: "Dark" }) },
      ]}
      onValueChange={(value) => setChoice(value as ThemeChoice)}
    />
  );
}
