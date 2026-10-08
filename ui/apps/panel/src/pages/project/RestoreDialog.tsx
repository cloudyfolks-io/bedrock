import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { Button, Dialog, Select, Switch, TextField, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { kubeClient, listKey } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { Ltr } from "../../components/Ltr";
import { listAt } from "../../resources/path";
import { definitionFor, refOf } from "../../resources/registry";
import { restoreBody } from "./restoreBody";

interface Entry { id: string; time: string; sizeBytes?: number }

export function RestoreDialog({ backup, onClose }: { backup: KubeObject; onClose: () => void }) {
  const { t } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const queryClient = useQueryClient();
  const def = definitionFor("virtualmachinerestores");
  const entries = listAt(backup, "status.backups") as Entry[];
  const vmRef = String(backup.spec?.vmRef ?? "");
  const [backupID, setBackupID] = useState(entries[0]?.id ?? "");
  const [replace, setReplace] = useState(false);
  const [typed, setTyped] = useState("");
  const ready = backupID !== "" && (!replace || typed === vmRef);

  async function restore() {
    if (!def) {
      return;
    }
    try {
      await kubeClient(http).create(refOf(def), backup.metadata.namespace, restoreBody(backup, { backupID, replace, suffix: Date.now().toString(36) }));
      toast.show({ tone: "success", title: t({ id: "backups.restore.done", message: "Restore started" }) });
      void queryClient.invalidateQueries({ queryKey: listKey(refOf(def), backup.metadata.namespace, {}) });
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
      title={t({ id: "backups.restore.title", message: `Restore ${backup.metadata.name}` })}
      actions={
        <Button variant="primary" disabled={!ready} onClick={() => void restore()}>
          {t({ id: "backups.restore.confirm", message: "Restore" })}
        </Button>
      }
    >
      <Select
        label={t({ id: "backups.restore.id", message: "Backup" })}
        value={backupID}
        onValueChange={setBackupID}
        options={entries.map((entry) => ({ value: entry.id, label: `${entry.id} (${entry.time})` }))}
      />
      <p>
        {t({ id: "backups.restore.target", message: "The restored machine is named" })} <Ltr>{vmRef}</Ltr>
      </p>
      <Switch label={t({ id: "backups.restore.replace", message: "Replace the existing machine" })} checked={replace} onCheckedChange={setReplace} />
      {replace ? (
        <TextField label={t({ id: "backups.restore.typed", message: `Type ${vmRef} to confirm` })} name="confirm-replace" value={typed} onChange={setTyped} dir="ltr" autoComplete="off" />
      ) : null}
    </Dialog>
  );
}
