import { useCallback, useState } from "react";
import { Link } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Select } from "@bedrock/design";
import type { KubeObject } from "../../api/types";
import { textAt } from "../../resources/path";
import { definitionFor } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";

const phases = ["Running", "Stopped", "Starting", "Stopping", "Migrating", "Failed"];

export default function VmsPage() {
  const { t } = useLingui();
  const def = definitionFor("virtualmachines");
  const [phase, setPhase] = useState("");
  const itemFilter = useCallback((object: KubeObject) => phase === "" || textAt(object, "status.phase") === phase, [phase]);
  if (!def) {
    return null;
  }
  return (
    <ResourceListPage
      def={def}
      itemFilter={itemFilter}
      toolbar={
        <Select
          label={t({ id: "vms.filter.phase", message: "Phase" })}
          value={phase}
          onValueChange={setPhase}
          options={[{ value: "", label: t({ id: "vms.filter.all", message: "All phases" }) }, ...phases.map((value) => ({ value, label: value }))]}
        />
      }
      actions={({ canCreate, project }) =>
        canCreate && project ? (
          <Link className="br-button br-button--secondary" to={`/p/${project.name}/vms/new-iso`}>
            {t({ id: "vms.installIso", message: "Install from ISO" })}
          </Link>
        ) : null
      }
    />
  );
}
