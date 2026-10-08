import { useLingui } from "@lingui/react/macro";
import { useParams } from "react-router";
import { MetricChart } from "./MetricChart";

export default function HostMetrics() {
  const { t } = useLingui();
  const { name = "" } = useParams();
  return (
    <>
      <MetricChart title={t({ id: "metrics.cpu", message: "CPU use" })} queries={["host.cpu"]} unit="ratio" name={name} />
      <MetricChart title={t({ id: "metrics.memory", message: "Memory use" })} queries={["host.memory"]} unit="ratio" name={name} />
      <MetricChart title={t({ id: "metrics.network", message: "Network traffic" })} queries={["host.network.rx", "host.network.tx"]} unit="bytesPerSecond" name={name} />
    </>
  );
}
