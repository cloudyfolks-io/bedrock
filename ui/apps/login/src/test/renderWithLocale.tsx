import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { i18n } from "@lingui/core";
import { I18nProvider } from "@lingui/react";
import { activate, type Locale } from "../i18n";

export function renderWithLocale(node: ReactElement, locale: Locale) {
  activate(locale);
  return render(<I18nProvider i18n={i18n}>{node}</I18nProvider>);
}
