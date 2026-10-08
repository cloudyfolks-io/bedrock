import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow } from "@bedrock/design";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { Ltr } from "../../components/Ltr";
import { PageHeader } from "../../components/PageHeader";
import { formatBytes, formatDateTime } from "../../format";
import { useLocale } from "../../LocaleProvider";
import { listAt } from "../../resources/path";
import { definitionFor, refOf } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { useCurrentProject } from "../../shell/context";
import { useLiveList } from "../../stream/live";
import { RestoreDialog } from "./RestoreDialog";

interface Entry { id: string; time: string; sizeBytes?: number }

function History({ backups }: { backups: KubeObject[] }) {
  const { t } = useLingui();
  const { locale } = useLocale();
  return (
    <>
      {backups.map((backup) => (
        <Table key={backup.metadata.name} caption={t({ id: "backups.history", message: `Backups of ${backup.metadata.name}` })}>
          <TableHead>
            <TableRow>
              <TableHeaderCell>ID</TableHeaderCell>
              <TableHeaderCell>{t({ id: "upgrades.backup.time", message: "Time" })}</TableHeaderCell>
              <TableHeaderCell>{t({ id: "col.size", message: "Size" })}</TableHeaderCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {(listAt(backup, "status.backups") as Entry[]).map((entry) => (
              <TableRow key={entry.id}>
                <TableCell><Ltr>{entry.id}</Ltr></TableCell>
                <TableCell>{formatDateTime(entry.time, locale)}</TableCell>
                <TableCell>{entry.sizeBytes ? formatBytes(entry.sizeBytes, locale) : "—"}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      ))}
    </>
  );
}

export default function BackupsPage() {
  const { t } = useLingui();
  const backups = definitionFor("virtualmachinebackups");
  const restores = definitionFor("virtualmachinerestores");
  const project = useCurrentProject();
  const [restoring, setRestoring] = useState<KubeObject | undefined>();
  const [canRestore] = useCans(project ? [{ verb: "create", group: "bedrock.cloudyfolks.io", resource: "virtualmachinerestores", namespace: project.namespace }] : []);
  const list = useLiveList(backups ? refOf(backups) : { group: "", version: "", plural: "", namespaced: true }, project ? { namespace: project.namespace } : {});
  if (!backups || !restores) {
    return null;
  }
  return (
    <>
      <PageHeader title={t({ id: "nav.item.backups", message: "Backups" })} />
      <ResourceListPage
        def={backups}
        embedded
        rowItems={(object) =>
          canRestore === "allowed" && listAt(object, "status.backups").length > 0
            ? [{ id: "restore", label: t({ id: "backups.restore.confirm", message: "Restore" }), onSelect: () => setRestoring(object) }]
            : []
        }
      >
        {restoring ? <RestoreDialog backup={restoring} onClose={() => setRestoring(undefined)} /> : null}
      </ResourceListPage>
      <History backups={list.data?.items ?? []} />
      <ResourceListPage def={restores} embedded readOnly />
    </>
  );
}
