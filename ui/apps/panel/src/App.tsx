import { useEffect } from "react";
import { i18n } from "@lingui/core";
import { I18nProvider } from "@lingui/react";
import { useLingui } from "@lingui/react/macro";
import { activate, pickLocale, readStoredLocale } from "./i18n";

function Title() {
  const { t } = useLingui();
  return <h1>{t({ id: "app.title", message: "Bedrock" })}</h1>;
}

export function App() {
  useEffect(() => {
    activate(pickLocale(readStoredLocale(), navigator.languages));
  }, []);

  return (
    <I18nProvider i18n={i18n}>
      <Title />
    </I18nProvider>
  );
}
