import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { definitionFor } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { useCurrentProject } from "../../shell/context";
import { ExpandVolumeDialog } from "./ExpandVolumeDialog";

export default function VolumesPage() {
  const { t } = useLingui();
  const def = definitionFor("volumes");
  const project = useCurrentProject();
  const [expanding, setExpanding] = useState<KubeObject | undefined>();
  const [canUpdate] = useCans(project && def ? [{ verb: "update", group: def.group, resource: def.plural, namespace: project.namespace }] : []);
  if (!def) {
    return null;
  }
  return (
    <ResourceListPage
      def={def}
      rowItems={(object) => (canUpdate === "allowed" ? [{ id: "expand", label: t({ id: "volumes.expand.confirm", message: "Expand" }), onSelect: () => setExpanding(object) }] : [])}
    >
      {expanding ? <ExpandVolumeDialog volume={expanding} onClose={() => setExpanding(undefined)} /> : null}
    </ResourceListPage>
  );
}
