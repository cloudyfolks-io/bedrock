import { useLingui } from "@lingui/react/macro";

export default function HostMetrics() {
  const { t } = useLingui();
  return <p>{t({ id: "metrics.placeholder", message: "Metrics" })}</p>;
}
