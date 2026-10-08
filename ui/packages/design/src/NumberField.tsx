import { NumberField as BaseNumberField } from "@base-ui/react/number-field";

interface NumberFieldProps {
  label: string;
  value: number | null;
  onValueChange: (value: number | null) => void;
  min?: number;
  max?: number;
  step?: number;
  error?: string;
  readOnly?: boolean | undefined;
  disabled?: boolean | undefined;
}

export function NumberField({ label, value, onValueChange, min, max, step, error, readOnly, disabled }: NumberFieldProps) {
  return (
    <BaseNumberField.Root
      className="br-field"
      value={value}
      onValueChange={onValueChange}
      min={min}
      max={max}
      step={step}
      readOnly={readOnly}
      disabled={disabled}
    >
      <span className="br-field-label">{label}</span>
      <BaseNumberField.Group className="br-number">
        <BaseNumberField.Decrement className="br-number-step" aria-label={`${label} -`}>
          −
        </BaseNumberField.Decrement>
        <BaseNumberField.Input className="br-number-input" aria-label={label} aria-invalid={error ? true : undefined} />
        <BaseNumberField.Increment className="br-number-step" aria-label={`${label} +`}>
          +
        </BaseNumberField.Increment>
      </BaseNumberField.Group>
      {error ? <span className="br-field-error">{error}</span> : null}
    </BaseNumberField.Root>
  );
}
