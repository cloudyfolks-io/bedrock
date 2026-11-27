import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Button, Dialog, NumberField, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { kubeClient } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { Ltr } from "../../components/Ltr";
import { textAt } from "../../resources/path";
import { definitionFor, refOf } from "../../resources/registry";
import { canExpand } from "./volumeHelpers";
import styles from "./storage.module.css";

export function ExpandVolumeDialog({ volume, onClose }: { volume: KubeObject; onClose: () => void }) {
  const { t } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const def = definitionFor("volumes");
  const current = Number(volume.spec?.sizeGi ?? 0);
  const attachedTo = textAt(volume, "status.attachedTo");
  const [size, setSize] = useState<number | null>(current + 1);
  const valid = size !== null && canExpand(volume, size);

  async function expand() {
    if (!def || size === null || !valid) {
      return;
    }
    try {
      await kubeClient(http).mergePatch(refOf(def), volume.metadata.namespace, volume.metadata.name, { spec: { sizeGi: size } });
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
      <div className={styles.fields}>
        <dl className={styles.facts}>
          <div><dt>{t({ id: "volumes.expand.currentSize", message: "Current size" })}</dt><dd><Ltr>{`${current} GiB`}</Ltr></dd></div>
          <div><dt>{t({ id: "col.attachedTo", message: "Attached to" })}</dt><dd>{attachedTo ? <Ltr>{attachedTo}</Ltr> : "—"}</dd></div>
        </dl>
        <NumberField
          label={t({ id: "volumes.expand.size", message: "New size (GiB)" })}
          value={size}
          onValueChange={setSize}
          min={1}
          {...(!valid ? { error: t({ id: "volumes.expand.error", message: `Choose a size above ${current} GiB` }) } : {})}
        />
      </div>
    </Dialog>
  );
}
