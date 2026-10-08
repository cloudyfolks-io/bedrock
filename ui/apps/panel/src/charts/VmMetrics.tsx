import { useLingui } from "@lingui/react/macro";
import type { KubeObject } from "../api/types";
import { MetricChart } from "./MetricChart";

export default function VmMetrics({ vm }: { vm: KubeObject }) {
  const { t } = useLingui();
  const scope = { namespace: vm.metadata.namespace, name: vm.metadata.name };
  return (
    <>
      <MetricChart title={t({ id: "metrics.cpu", message: "CPU use" })} queries={["vm.cpu"]} unit="cores" {...scope} />
      <MetricChart title={t({ id: "metrics.memory", message: "Memory use" })} queries={["vm.memory"]} unit="bytes" {...scope} />
      <MetricChart title={t({ id: "metrics.network", message: "Network traffic" })} queries={["vm.network.rx", "vm.network.tx"]} unit="bytesPerSecond" {...scope} />
      <MetricChart title={t({ id: "metrics.disk", message: "Disk traffic" })} queries={["vm.disk.read", "vm.disk.write"]} unit="bytesPerSecond" {...scope} />
    </>
  );
}
