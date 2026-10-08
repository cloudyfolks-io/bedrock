import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { textAt } from "../../resources/path";
import { definitionFor } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { useCurrentProject } from "../../shell/context";
import { CredentialsDialog } from "./CredentialsDialog";

export default function ObjectUsersPage() {
  const { t } = useLingui();
  const def = definitionFor("objectstorageusers");
  const project = useCurrentProject();
  const [showing, setShowing] = useState<KubeObject | undefined>();
  const [canRead] = useCans(project ? [{ verb: "get", group: "", resource: "secrets", namespace: project.namespace }] : []);
  if (!def) {
    return null;
  }
  return (
    <ResourceListPage
      def={def}
      rowItems={(object) =>
        canRead === "allowed" && textAt(object, "status.credentialsSecret") !== ""
          ? [{ id: "credentials", label: t({ id: "users.showCredentials", message: "Show credentials" }), onSelect: () => setShowing(object) }]
          : []
      }
    >
      {showing && project ? <CredentialsDialog namespace={project.namespace} secretName={textAt(showing, "status.credentialsSecret")} open onClose={() => setShowing(undefined)} /> : null}
    </ResourceListPage>
  );
}
