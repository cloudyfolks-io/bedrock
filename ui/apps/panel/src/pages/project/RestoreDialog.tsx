import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { useQuery } from "@tanstack/react-query";
import { Alert, Button, Dialog, Select, Switch, TextField, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { isKubeStatusError } from "../../api/http";
import { kubeClient } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { Ltr } from "../../components/Ltr";
import { listAt } from "../../resources/path";
import { definitionFor, refOf } from "../../resources/registry";
import { restoreBody } from "./restoreBody";
import styles from "./storage.module.css";

interface Entry { id: string; time: string; sizeBytes?: number }
type RestoreTarget = { kind: "exists"; uid: string } | { kind: "missing" | "unknown" };

export function RestoreDialog({ backup, onClose }: { backup: KubeObject; onClose: () => void }) {
  const { t } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const def = definitionFor("virtualmachinerestores");
  const backupDef = definitionFor("virtualmachinebackups");
  const vmDef = definitionFor("virtualmachines");
  const entries = listAt(backup, "status.backups") as Entry[];
  const vmRef = typeof backup.spec?.vmRef === "string" ? backup.spec.vmRef : "";
  const namespace = backup.metadata.namespace;
  const [backupID, setBackupID] = useState(entries[0]?.id ?? "");
  const [replace, setReplace] = useState(false);
  const [confirmation, setConfirmation] = useState<{ uid: string; text: string }>();
  const [sourceChanged, setSourceChanged] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const target = useQuery({
    queryKey: ["restore-target", namespace, vmRef],
    enabled: Boolean(vmDef && namespace && vmRef),
    retry: false,
    networkMode: "always",
    refetchOnMount: "always",
    queryFn: async (): Promise<RestoreTarget> => {
      if (!vmDef || !namespace || !vmRef) {
        return { kind: "unknown" };
      }
      try {
        const { metadata } = (await kubeClient(http).get(refOf(vmDef), namespace, vmRef)) ?? {};
        return metadata?.name === vmRef && metadata.namespace === namespace && typeof metadata.uid === "string" && metadata.uid
          ? { kind: "exists", uid: metadata.uid }
          : { kind: "unknown" };
      } catch (error) {
        if (isKubeStatusError(error) && error.status === 404 && error.reason === "NotFound") {
          return { kind: "missing" };
        }
        throw error;
      }
    },
  });
  const targetState = target.isFetching ? "checking" : target.isError ? "error" : target.isFetchedAfterMount ? target.data?.kind : "unknown";
  const targetUID = target.data?.kind === "exists" ? target.data.uid : undefined;
  const typed = confirmation && confirmation.uid === targetUID ? confirmation.text : "";
  const targetChanged = targetState === "exists" && replace && Boolean(confirmation?.text && confirmation.uid !== targetUID);
  const ready = !submitting && !sourceChanged && entries.some((entry) => entry.id === backupID)
    && (replace ? targetState === "exists" && typed === vmRef : targetState === "missing");

  async function restore() {
    if (!ready || !def) {
      return;
    }
    setSubmitting(true);
    try {
      if (replace) {
        // The controller resolves replacement from the current backup, not the dialog's copy.
        const source = backupDef && await kubeClient(http).get(refOf(backupDef), namespace, backup.metadata.name);
        if (!source || !backup.metadata.uid || source.metadata?.uid !== backup.metadata.uid || source.spec?.vmRef !== vmRef
          || !(listAt(source, "status.backups") as Entry[]).some((entry) => entry.id === backupID)) {
          setSourceChanged(true);
          return;
        }
      }
      const fresh = await target.refetch();
      if (!fresh.isSuccess || (replace
        ? fresh.data.kind !== "exists" || fresh.data.uid !== confirmation?.uid
        : fresh.data.kind !== "missing")) {
        return;
      }
      // The name-based API cannot carry a UID precondition into reconciliation.
      await kubeClient(http).create(refOf(def), namespace, restoreBody(backup, { backupID, replace, suffix: Date.now().toString(36) }));
      toast.show({ tone: "success", title: t({ id: "backups.restore.done", message: "Restore started" }) });
      onClose();
    } catch (error) {
      toast.show({ tone: "danger", title: t({ id: "action.error.failed", message: "The action failed" }), description: error instanceof Error ? error.message : "" });
    } finally {
      setSubmitting(false);
    }
  }

  const targetMessage = {
    checking: t({ id: "backups.restore.target.checking", message: "Checking whether the target exists…" }),
    unknown: t({ id: "backups.restore.target.unknown", message: "The target virtual machine could not be identified." }),
    error: t({ id: "backups.restore.target.error", message: "Could not check the target virtual machine." }),
    exists: targetChanged
      ? t({ id: "backups.restore.target.changed", message: "The target changed. Confirm the name again to replace the current machine." })
      : replace
        ? t({ id: "backups.restore.target.overwrite", message: "This restore will overwrite the existing machine." })
        : t({ id: "backups.restore.target.exists", message: "The target already exists. Choose replacement to restore over it." }),
    missing: replace
      ? t({ id: "backups.restore.target.replaceMissing", message: "Replacement requires an existing target. Turn replacement off to restore this machine." })
      : t({ id: "backups.restore.target.missing", message: "The target does not exist. This restore will create it." }),
  }[targetState ?? "unknown"];

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next && !submitting) {
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
      <div className={styles.fields}>
        <Select
          label={t({ id: "backups.restore.id", message: "Backup" })}
          value={backupID}
          onValueChange={setBackupID}
          options={entries.map((entry) => ({ value: entry.id, label: `${entry.id} (${entry.time})` }))}
          disabled={submitting}
        />
        {vmRef ? (
          <p>{t({ id: "backups.restore.target", message: "The restored machine is named" })} <Ltr>{vmRef}</Ltr></p>
        ) : null}
        {sourceChanged ? <Alert tone="error">{t({ id: "backups.restore.source.changed", message: "The backup changed. Close this dialog and select the backup again." })}</Alert> : null}
        <Alert tone={targetState === "error" ? "error" : "info"}>{targetMessage}</Alert>
        {targetState === "error" ? (
          <div><Button variant="secondary" disabled={submitting} onClick={() => void target.refetch()}>
            {t({ id: "common.action.retry", message: "Retry" })}
          </Button></div>
        ) : null}
        <div className={`${styles.fields} ${styles.rule}`}>
          <Switch
            label={t({ id: "backups.restore.replace", message: "Replace the existing machine" })}
            checked={replace}
            disabled={submitting}
            onCheckedChange={(next) => {
              setReplace(next);
              setConfirmation(undefined);
            }}
          />
          {replace && vmRef ? (
            <TextField
              label={t({ id: "backups.restore.typed", message: `Type ${vmRef} to confirm` })}
              name="confirm-replace"
              value={typed}
              onChange={(text) => setConfirmation(targetUID ? { uid: targetUID, text } : undefined)}
              dir="ltr"
              autoComplete="off"
              disabled={submitting || sourceChanged || targetState !== "exists"}
            />
          ) : null}
        </div>
      </div>
    </Dialog>
  );
}
