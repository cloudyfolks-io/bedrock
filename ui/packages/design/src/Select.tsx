import { useId } from "react";
import { Select as BaseSelect } from "@base-ui/react/select";

interface SelectOption {
  value: string;
  label: string;
}

interface SelectProps {
  label: string;
  value: string;
  onValueChange: (value: string) => void;
  options: SelectOption[];
  placeholder?: string;
  error?: string;
  disabled?: boolean | undefined;
}

export function Select({ label, value, onValueChange, options, placeholder, error, disabled }: SelectProps) {
  const labelId = useId();
  const errorId = useId();
  return (
    <div className="br-field">
      <span id={labelId} className="br-field-label">
        {label}
      </span>
      <BaseSelect.Root
        items={options}
        disabled={disabled}
        value={value === "" ? null : value}
        onValueChange={(next) => onValueChange(next === null ? "" : String(next))}
      >
        <BaseSelect.Trigger
          className="br-select-trigger"
          aria-labelledby={labelId}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? errorId : undefined}
        >
          <BaseSelect.Value placeholder={placeholder ?? ""} />
          <BaseSelect.Icon aria-hidden="true">▾</BaseSelect.Icon>
        </BaseSelect.Trigger>
        <BaseSelect.Portal>
          <BaseSelect.Positioner>
            <BaseSelect.Popup className="br-select-popup">
              <BaseSelect.List>
                {options.map((option) => (
                  <BaseSelect.Item key={option.value} value={option.value} className="br-select-item">
                    <BaseSelect.ItemText>{option.label}</BaseSelect.ItemText>
                  </BaseSelect.Item>
                ))}
              </BaseSelect.List>
            </BaseSelect.Popup>
          </BaseSelect.Positioner>
        </BaseSelect.Portal>
      </BaseSelect.Root>
      {error ? (
        <span id={errorId} className="br-field-error">
          {error}
        </span>
      ) : null}
    </div>
  );
}
