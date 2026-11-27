import { useLingui } from "@lingui/react/macro";
import { definitionFor } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import styles from "../operator.module.css";

export default function HostsPage() {
  const { t } = useLingui();
  const def = definitionFor("hosts");
  if (!def) {
    return null;
  }
  return (
    <ResourceListPage
      def={def}
      help={
        <p className={`${styles.surface} ${styles.sectionBody} ${styles.help} ${styles.hostHelp}`}>
          {t({ id: "hosts.help", message: "Hosts join the cluster with the command" })} <code dir="ltr">bedrock join</code>
        </p>
      }
    />
  );
}
