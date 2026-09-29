import { useEffect, useState } from "react";
import { i18n } from "@lingui/core";
import { I18nProvider } from "@lingui/react";
import { LanguageMenu, ThemeProvider } from "@bedrock/design";
import { Account } from "./account/Account";
import * as api from "./api";
import { activate, pickLocale, readStoredLocale, type Locale } from "./i18n";
import { authRequestFromLocation, routeFor } from "./router";
import { DeviceEntry } from "./screens/DeviceEntry";
import { screenFor } from "./screens";
import type { Challenge } from "./types";

const languageOptions = [
  { value: "en", label: "English" },
  { value: "fa", label: "فارسی" },
  { value: "ar", label: "العربية" },
];

function loadChallenge(search: string): Promise<Challenge> {
  const authRequest = authRequestFromLocation(search);
  return authRequest ? api.start(authRequest) : api.challenge();
}

export function App() {
  const [locale, setLocale] = useState<Locale>(() => pickLocale(readStoredLocale(), navigator.languages));
  const [challenge, setChallenge] = useState<Challenge | null>(null);
  const route = routeFor(window.location.pathname);

  useEffect(() => {
    activate(locale);
  }, [locale]);

  useEffect(() => {
    if (route !== "device" && route !== "account") {
      loadChallenge(window.location.search).then(setChallenge);
    }
  }, [route]);

  useEffect(() => {
    if ((challenge?.type === "redirect" || challenge?.type === "done") && challenge.redirect) {
      window.location.assign(challenge.redirect);
    }
  }, [challenge]);

  const Screen = challenge ? screenFor(challenge) : null;

  return (
    <ThemeProvider>
      <I18nProvider i18n={i18n}>
        <LanguageMenu value={locale} onChange={(value) => setLocale(value as Locale)} options={languageOptions} />
        {route === "account" ? <Account /> : null}
        {route === "device" && !challenge ? <DeviceEntry onChallenge={setChallenge} /> : null}
        {route !== "account" && Screen && challenge ? <Screen challenge={challenge} onChallenge={setChallenge} /> : null}
      </I18nProvider>
    </ThemeProvider>
  );
}
