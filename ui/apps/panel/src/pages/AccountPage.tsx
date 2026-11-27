import { useLingui } from "@lingui/react/macro";
import { PageHeader } from "../components/PageHeader";
import { LanguageSelect } from "../shell/Preferences";
import styles from "./operator.module.css";

export function AccountPage() {
  const { t } = useLingui();
  return (
    <div className={styles.stack}>
      <PageHeader title={t({ id: "account.title", message: "Account" })} />
      <div className={`${styles.stack} ${styles.account}`}>
        <div className={`${styles.surface} ${styles.sectionBody}`}>
          <LanguageSelect />
        </div>
        <section className={styles.frame} aria-labelledby="account-downloads">
          <div className={styles.frameHeader}>
            <h2 id="account-downloads">{t({ id: "account.downloads.title", message: "Downloads" })}</h2>
          </div>
          <ul className={`${styles.layer} ${styles.downloads}`} role="list">
            <li className={styles.sectionBody}>
              <p id="download-kubeconfig">{t({ id: "account.downloads.kubeconfigDescription", message: "Configure kubectl to connect to this Bedrock cluster." })}</p>
              <a className="br-button br-button--secondary" href="/kubeconfig" download aria-describedby="download-kubeconfig">
                {t({ id: "shell.userMenu.kubeconfig", message: "Download kubeconfig" })}
              </a>
            </li>
            <li className={styles.sectionBody}>
              <p id="download-ca">{t({ id: "account.downloads.caDescription", message: "Use this certificate to trust TLS connections to a cluster that uses a private CA." })}</p>
              <a className="br-button br-button--secondary" href="/ca.crt" download aria-describedby="download-ca">
                {t({ id: "shell.userMenu.ca", message: "Download CA certificate" })}
              </a>
            </li>
          </ul>
        </section>
      </div>
    </div>
  );
}
