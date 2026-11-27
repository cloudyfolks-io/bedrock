import { useState } from "react";
import { msg } from "@lingui/core/macro";
import { useLingui } from "@lingui/react/macro";
import { Button, Dialog, NumberField, TextField, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { kubeClient } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { Ltr } from "../../components/Ltr";
import { formatBytes } from "../../format";
import { useLocale } from "../../LocaleProvider";
import type { ColumnDef } from "../../resources/columns";
import { definitionFor, refOf } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { useCurrentProject } from "../../shell/context";
import styles from "./storage.module.css";

const dnsLabel = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

const snapshotSize: ColumnDef = {
  id: "size",
  label: msg({ id: "col.size", message: "Size" }),
  render: (object, { locale }) => typeof object.status?.sizeBytes === "number" ? formatBytes(object.status.sizeBytes, locale) : "—",
  sortValue: (object) => Number(object.status?.sizeBytes ?? 0),
};

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
  const { locale } = useLocale();
  const http = useHttp();
  const toast = useToast();
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
      <div className={styles.fields}>
        <dl className={styles.facts}>
          <div><dt>{t({ id: "col.source", message: "Source" })}</dt><dd><Ltr>{snapshot.metadata.name}</Ltr></dd></div>
          <div><dt>{t({ id: "col.size", message: "Size" })}</dt><dd>{bytes > 0 ? formatBytes(bytes, locale) : "—"}</dd></div>
        </dl>
        <TextField label={t({ id: "forms.name.label", message: "Name" })} name="volume-name" value={name} onChange={setName} dir="ltr" autoComplete="off" />
        <NumberField label={t({ id: "volumes.expand.size", message: "New size (GiB)" })} value={size} onValueChange={setSize} min={1} />
      </div>
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
      def={{ ...def, columns: def.columns.flatMap((column) => column.id === "phase" ? [column, snapshotSize] : [column]) }}
      rowItems={(object) =>
        canCreate === "allowed" ? [{ id: "restore", label: t({ id: "snapshots.restore.title", message: "Restore to a new volume" }), disabled: object.status?.phase !== "Ready", onSelect: () => setRestoring(object) }] : []
      }
    >
      {restoring ? <RestoreDialog snapshot={restoring} onClose={() => setRestoring(undefined)} /> : null}
    </ResourceListPage>
  );
}
