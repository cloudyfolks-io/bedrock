import { Link } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { isHttpError } from "../../api/http";
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
      {...(project ? {
        listPath: `/proxy/v1/projects/${encodeURIComponent(project.name)}/networks`,
        createSpec: { tenantRef: project.tenant },
        lockedPaths: ["tenantRef"],
      } : {})}
      listFailure={(error) => isHttpError(error) && error.code === "network_inventory_limit" ? (
        <EmptyState title={t({ id: "networks.limit", message: "The network list is too large" })} />
      ) : null}
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
