import { Checkbox as BaseCheckbox } from "@base-ui/react/checkbox";

interface CheckboxProps {
  label: string;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
  disabled?: boolean;
}

export function Checkbox({ label, checked, onCheckedChange, disabled }: CheckboxProps) {
  return (
    <label className="br-check" data-disabled={disabled ? "" : undefined}>
      <BaseCheckbox.Root
        className="br-checkbox"
        checked={checked}
        onCheckedChange={onCheckedChange}
        disabled={disabled}
      >
        <BaseCheckbox.Indicator>✓</BaseCheckbox.Indicator>
      </BaseCheckbox.Root>
      {label}
    </label>
  );
}
