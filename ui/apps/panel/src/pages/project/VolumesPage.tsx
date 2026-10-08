import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { Button, Dialog, NumberField, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { kubeClient, listKey } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { definitionFor, refOf } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { useCurrentProject } from "../../shell/context";
import { canExpand } from "./volumeHelpers";

function ExpandDialog({ volume, onClose }: { volume: KubeObject; onClose: () => void }) {
  const { t } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const queryClient = useQueryClient();
  const def = definitionFor("volumes");
  const current = Number(volume.spec?.sizeGi ?? 0);
  const [size, setSize] = useState<number | null>(current + 1);
  const valid = size !== null && canExpand(volume, size);

  async function expand() {
    if (!def || size === null) {
      return;
    }
    try {
      await kubeClient(http).mergePatch(refOf(def), volume.metadata.namespace, volume.metadata.name, { spec: { sizeGi: size } });
      void queryClient.invalidateQueries({ queryKey: listKey(refOf(def), volume.metadata.namespace, {}) });
      onClose();
    } catch (error) {
      toast.show({ tone: "danger", title: t({ id: "action.error.failed", message: "The action failed" }), description: error instanceof Error ? error.message : "" });
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) {
          onClose();
        }
      }}
      title={t({ id: "volumes.expand.title", message: `Expand ${volume.metadata.name}` })}
      description={t({ id: "volumes.expand.description", message: "A volume can only grow." })}
      actions={
        <Button variant="primary" disabled={!valid} onClick={() => void expand()}>
          {t({ id: "volumes.expand.confirm", message: "Expand" })}
        </Button>
      }
    >
      <NumberField
        label={t({ id: "volumes.expand.size", message: "New size (GiB)" })}
        value={size}
        onValueChange={setSize}
        min={1}
        {...(!valid ? { error: t({ id: "volumes.expand.error", message: `Choose a size above ${current} GiB` }) } : {})}
      />
    </Dialog>
  );
}

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
      {expanding ? <ExpandDialog volume={expanding} onClose={() => setExpanding(undefined)} /> : null}
    </ResourceListPage>
  );
}
