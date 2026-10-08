import { Link } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { EmptyState } from "../../components/EmptyState";
import { definitionFor } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { useCurrentProject } from "../../shell/context";

export default function NetworksPage() {
  const { t } = useLingui();
  const def = definitionFor("networks");
  const project = useCurrentProject();
  if (!def) {
    return null;
  }
  return (
    <ResourceListPage
      def={def}
      {...(project ? { createSpec: { tenantRef: project.tenant }, lockedPaths: ["tenantRef"] } : {})}
      forbidden={
        <EmptyState
          title={t({ id: "networks.forbidden.title", message: "You cannot list networks" })}
          description={t({ id: "networks.forbidden.description", message: "Networks belong to the tenant. Subnets of your project still work." })}
          action={
            project ? (
              <Link className="br-button br-button--secondary" to={`/p/${project.name}/subnets`}>
                {t({ id: "nav.item.subnets", message: "Subnets" })}
              </Link>
            ) : null
          }
        />
      }
    />
  );
}
