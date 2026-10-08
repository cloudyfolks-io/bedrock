import { useLingui } from "@lingui/react/macro";
import { definitionFor } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";

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
        <p className="br-help">
          {t({ id: "hosts.help", message: "Hosts join the cluster with the command" })} <code dir="ltr">bedrock join</code>
        </p>
      }
    />
  );
}
