import { useCallback, useState } from "react";
import { Link } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Select } from "@bedrock/design";
import type { KubeObject } from "../../api/types";
import { phaseMessages } from "../../components/StatusBadge";
import { textAt } from "../../resources/path";
import { definitionFor } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";

const phases = ["Running", "Stopped", "Starting", "Stopping", "Migrating", "Failed"];

export default function VmsPage() {
  const { t, i18n } = useLingui();
  const def = definitionFor("virtualmachines");
  const [phase, setPhase] = useState("");
  const itemFilter = useCallback((object: KubeObject) => phase === "" || textAt(object, "status.phase") === phase, [phase]);
  const allPhases = t({ id: "vms.filter.all", message: "All phases" });
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
          placeholder={allPhases}
          onValueChange={setPhase}
          options={[{ value: "", label: allPhases }, ...phases.map((value) => {
            const message = phaseMessages[value];
            return { value, label: message ? i18n._(message) : value };
          })]}
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
