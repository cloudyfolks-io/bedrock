import { Switch as BaseSwitch } from "@base-ui/react/switch";

interface SwitchProps {
  label: string;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
  disabled?: boolean;
}

export function Switch({ label, checked, onCheckedChange, disabled }: SwitchProps) {
  return (
    <label className="br-check" data-disabled={disabled ? "" : undefined}>
      <BaseSwitch.Root className="br-switch" checked={checked} onCheckedChange={onCheckedChange} disabled={disabled}>
        <BaseSwitch.Thumb className="br-switch-thumb" />
      </BaseSwitch.Root>
      {label}
    </label>
  );
}
