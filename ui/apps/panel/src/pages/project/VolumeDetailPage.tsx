import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Button } from "@bedrock/design";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { MetricChart } from "../../charts/MetricChart";
import { definitionFor } from "../../resources/registry";
import { ResourceDetailPage } from "../../resources/ResourceDetailPage";
import { ExpandVolumeDialog } from "./ExpandVolumeDialog";
import styles from "./storage.module.css";

const def = definitionFor("volumes");

function ExpandAction({ volume, onSelect }: { volume: KubeObject; onSelect: () => void }) {
  const { t } = useLingui();
  const { name, namespace } = volume.metadata;
  const [canUpdate] = useCans(def && namespace ? [{ verb: "update", group: def.group, resource: def.plural, namespace, name }] : []);
  return canUpdate === "allowed" ? (
    <Button variant="secondary" onClick={onSelect}>{t({ id: "volumes.expand.confirm", message: "Expand" })}</Button>
  ) : null;
}

export default function VolumeDetailPage() {
  const { t } = useLingui();
  const [expanding, setExpanding] = useState<KubeObject | undefined>();
  if (!def) {
    return null;
  }
  return (
    <ResourceDetailPage def={def} actions={(volume) => <ExpandAction volume={volume} onSelect={() => setExpanding(volume)} />}>
      {(volume) => (
        <div className={styles.volumeMetric}>
          <MetricChart
            title={t({ id: "metrics.volume", message: "Volume use" })}
            queries={["volume.used", "volume.capacity"]}
            unit="bytes"
            namespace={volume.metadata.namespace}
            name={volume.metadata.name}
          />
          {expanding && expanding.metadata.name === volume.metadata.name && expanding.metadata.namespace === volume.metadata.namespace && expanding.metadata.uid === volume.metadata.uid
            ? <ExpandVolumeDialog volume={volume} onClose={() => setExpanding(undefined)} />
            : null}
        </div>
      )}
    </ResourceDetailPage>
  );
}
