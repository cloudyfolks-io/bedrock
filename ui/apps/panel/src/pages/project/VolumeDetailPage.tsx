import { useLingui } from "@lingui/react/macro";
import { MetricChart } from "../../charts/MetricChart";
import { definitionFor } from "../../resources/registry";
import { ResourceDetailPage } from "../../resources/ResourceDetailPage";

export default function VolumeDetailPage() {
  const { t } = useLingui();
  const def = definitionFor("volumes");
  if (!def) {
    return null;
  }
  return (
    <ResourceDetailPage def={def}>
      {(volume) => (
        <MetricChart
          title={t({ id: "metrics.volume", message: "Volume use" })}
          queries={["volume.used", "volume.capacity"]}
          unit="bytes"
          namespace={volume.metadata.namespace}
          name={volume.metadata.name}
        />
      )}
    </ResourceDetailPage>
  );
}
