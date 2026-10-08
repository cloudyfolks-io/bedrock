import { useLingui } from "@lingui/react/macro";
import { Select } from "@bedrock/design";
import type { Range } from "../api/metrics";

export function RangePicker({ value, onChange }: { value: Range; onChange: (range: Range) => void }) {
  const { t } = useLingui();
  return (
    <Select
      label={t({ id: "metrics.range", message: "Time range" })}
      value={value}
      onValueChange={(next) => onChange(next as Range)}
      options={[
        { value: "1h", label: t({ id: "metrics.range.last1h", message: "Last hour" }) },
        { value: "6h", label: t({ id: "metrics.range.last6h", message: "Last 6 hours" }) },
        { value: "24h", label: t({ id: "metrics.range.last24h", message: "Last 24 hours" }) },
        { value: "7d", label: t({ id: "metrics.range.last7d", message: "Last 7 days" }) },
      ]}
    />
  );
}
