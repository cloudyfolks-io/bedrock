import { useLingui } from "@lingui/react/macro";
import { PageHeader } from "../components/PageHeader";
import { LanguageSelect, ThemeSelect } from "../shell/Preferences";

export function AccountPage() {
  const { t } = useLingui();
  return (
    <>
      <PageHeader title={t({ id: "account.title", message: "Account" })} />
      <section className="br-stack">
        <LanguageSelect />
        <ThemeSelect />
      </section>
      <section className="br-stack">
        <h2>{t({ id: "account.downloads.title", message: "Downloads" })}</h2>
        <a href="/kubeconfig" download>
          {t({ id: "shell.userMenu.kubeconfig", message: "Download kubeconfig" })}
        </a>
        <a href="/ca.crt" download>
          {t({ id: "shell.userMenu.ca", message: "Download CA certificate" })}
        </a>
      </section>
    </>
  );
}
