import { useState } from "react";
import { msg } from "@lingui/core/macro";
import { useLingui } from "@lingui/react/macro";
import { Select, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow } from "@bedrock/design";
import { conditionOf } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { Ltr } from "../../components/Ltr";
import { PageHeader } from "../../components/PageHeader";
import { formatBytes, formatDateTime } from "../../format";
import { useLocale } from "../../LocaleProvider";
import { listAt } from "../../resources/path";
import type { ColumnDef } from "../../resources/columns";
import { definitionFor } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { useCurrentProject } from "../../shell/context";
import { RestoreDialog } from "./RestoreDialog";
import styles from "./storage.module.css";

interface Entry { id: string; time: string; sizeBytes?: number }

function History({ backups }: { backups: readonly KubeObject[] }) {
  const { t } = useLingui();
  const { locale } = useLocale();
  const [selected, setSelected] = useState("");
  const backup = backups.find((item) => item.metadata.name === selected) ?? backups[0];
  if (!backup) {
    return null;
  }
  const entries = listAt(backup, "status.backups") as Entry[];
  return (
    <section className={styles.history}>
      <h3>{t({ id: "backups.history.title", message: "Backup history" })}</h3>
      {backups.length > 1 ? (
        <Select
          label={t({ id: "backups.history.schedule", message: "Backup schedule" })}
          value={backup.metadata.name}
          onValueChange={setSelected}
          options={backups.map(({ metadata: { name } }) => ({ value: name, label: name }))}
        />
      ) : null}
      <Table caption={t({ id: "backups.history", message: `Backups of ${backup.metadata.name}` })}>
        <TableHead>
          <TableRow>
            <TableHeaderCell>{t({ id: "backups.history.id", message: "Backup ID" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "upgrades.backup.time", message: "Time" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "col.size", message: "Size" })}</TableHeaderCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {entries.length === 0 ? (
            <TableRow><td className={`br-td ${styles.emptyRow}`} colSpan={3}>{t({ id: "list.empty.title", message: "Nothing here yet" })}</td></TableRow>
          ) : entries.map((entry) => (
            <TableRow key={entry.id}>
              <TableCell><Ltr>{entry.id}</Ltr></TableCell>
              <TableCell>{formatDateTime(entry.time, locale)}</TableCell>
              <TableCell>{typeof entry.sizeBytes === "number" ? formatBytes(entry.sizeBytes, locale) : "—"}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </section>
  );
}

const restoreReason: ColumnDef = {
  id: "reason",
  label: msg({ id: "detail.conditions.reason", message: "Reason" }),
  render: (restore) => {
    const phase = restore.status?.phase;
    const condition = conditionOf(restore, "Ready");
    return (phase === "Pending" || phase === "Failed") && condition?.status === "False" && (condition.reason || condition.message) ? (
      <div className={styles.reason}>
        {condition.reason ? <Ltr>{condition.reason}</Ltr> : null}
        {condition.message ? <span dir="auto">{condition.message}</span> : null}
      </div>
    ) : "—";
  },
};

export default function BackupsPage() {
  const { t } = useLingui();
  const backups = definitionFor("virtualmachinebackups");
  const restores = definitionFor("virtualmachinerestores");
  const project = useCurrentProject();
  const [restoring, setRestoring] = useState<KubeObject | undefined>();
  const [canRestore] = useCans(project ? [{ verb: "create", group: "bedrock.cloudyfolks.io", resource: "virtualmachinerestores", namespace: project.namespace }] : []);
  if (!backups || !restores) {
    return null;
  }
  return (
    <div className={styles.backups}>
      <PageHeader title={t({ id: "nav.item.backups", message: "Backups" })} />
      <ResourceListPage
        def={{ ...backups, title: msg({ id: "backups.schedules", message: "Backup schedules" }) }}
        embedded
        renderFiltered={(items) => <History backups={items} />}
        rowItems={(object) =>
          canRestore === "allowed" && listAt(object, "status.backups").length > 0
            ? [{ id: "restore", label: t({ id: "backups.restore.confirm", message: "Restore" }), onSelect: () => setRestoring(object) }]
            : []
        }
      >
        {restoring ? <RestoreDialog backup={restoring} onClose={() => setRestoring(undefined)} /> : null}
      </ResourceListPage>
      <ResourceListPage
        def={{
          ...restores,
          title: msg({ id: "inventory.vmRestores", message: "Virtual machine restores" }),
          columns: restores.columns.flatMap((column) => column.id === "phase" ? [column, restoreReason] : [column]),
        }}
        embedded
        readOnly
      />
    </div>
  );
}
