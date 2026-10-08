import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { Button, Dialog, NumberField, TextField, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { kubeClient, listKey } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { definitionFor, refOf } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { useCurrentProject } from "../../shell/context";

const dnsLabel = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

export function volumeFromSnapshot(snapshot: KubeObject, name: string, sizeGi: number): KubeObject {
  return {
    apiVersion: "bedrock.cloudyfolks.io/v1alpha1",
    kind: "Volume",
    metadata: { name, ...(snapshot.metadata.namespace === undefined ? {} : { namespace: snapshot.metadata.namespace }) },
    spec: { sizeGi, source: { snapshotRef: snapshot.metadata.name } },
  };
}

function RestoreDialog({ snapshot, onClose }: { snapshot: KubeObject; onClose: () => void }) {
  const { t } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const queryClient = useQueryClient();
  const volumes = definitionFor("volumes");
  const bytes = Number(snapshot.status?.sizeBytes ?? 0);
  const [name, setName] = useState("");
  const [size, setSize] = useState<number | null>(Math.max(1, Math.ceil(bytes / 1024 ** 3)));
  const nameValid = dnsLabel.test(name) && name.length <= 63;

  async function create() {
    if (!volumes || size === null) {
      return;
    }
    try {
      await kubeClient(http).create(refOf(volumes), snapshot.metadata.namespace, volumeFromSnapshot(snapshot, name, size));
      toast.show({ tone: "success", title: t({ id: "snapshots.restore.done", message: `Volume ${name} created` }) });
      void queryClient.invalidateQueries({ queryKey: listKey(refOf(volumes), snapshot.metadata.namespace, {}) });
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
      title={t({ id: "snapshots.restore.title", message: "Restore to a new volume" })}
      actions={
        <Button variant="primary" disabled={!nameValid || size === null} onClick={() => void create()}>
          {t({ id: "common.action.create", message: "Create" })}
        </Button>
      }
    >
      <TextField label={t({ id: "forms.name.label", message: "Name" })} name="volume-name" value={name} onChange={setName} dir="ltr" autoComplete="off" />
      <NumberField label={t({ id: "volumes.expand.size", message: "New size (GiB)" })} value={size} onValueChange={setSize} min={1} />
    </Dialog>
  );
}

export default function SnapshotsPage() {
  const { t } = useLingui();
  const def = definitionFor("volumesnapshots");
  const project = useCurrentProject();
  const [restoring, setRestoring] = useState<KubeObject | undefined>();
  const [canCreate] = useCans(project ? [{ verb: "create", group: "bedrock.cloudyfolks.io", resource: "volumes", namespace: project.namespace }] : []);
  if (!def) {
    return null;
  }
  return (
    <ResourceListPage
      def={def}
      rowItems={(object) =>
        canCreate === "allowed" ? [{ id: "restore", label: t({ id: "snapshots.restore.title", message: "Restore to a new volume" }), disabled: object.status?.phase !== "Ready", onSelect: () => setRestoring(object) }] : []
      }
    >
      {restoring ? <RestoreDialog snapshot={restoring} onClose={() => setRestoring(undefined)} /> : null}
    </ResourceListPage>
  );
}
