import { Progress as BaseProgress } from "@base-ui/react/progress";

interface ProgressProps {
  label: string;
  value: number;
  max: number;
}

export function Progress({ label, value, max }: ProgressProps) {
  return (
    <BaseProgress.Root className="br-progress" value={value} max={max} aria-label={label}>
      <BaseProgress.Label>{label}</BaseProgress.Label>
      <BaseProgress.Track className="br-progress-track">
        <BaseProgress.Indicator className="br-progress-bar" />
      </BaseProgress.Track>
    </BaseProgress.Root>
  );
}
