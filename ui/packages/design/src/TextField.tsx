import type { ChangeEvent } from "react";
import { Field } from "@base-ui/react/field";
import { Input } from "@base-ui/react/input";

interface TextFieldProps {
  label: string;
  name: string;
  type?: "text" | "password" | "email";
  autoComplete?: string;
  value: string;
  onChange: (value: string) => void;
  error?: string;
  readOnly?: boolean | undefined;
  disabled?: boolean | undefined;
  required?: boolean | undefined;
  placeholder?: string | undefined;
  dir?: "ltr" | "rtl" | "auto" | undefined;
}

export function TextField({ label, name, type = "text", autoComplete, value, onChange, error, readOnly, disabled, required, placeholder, dir }: TextFieldProps) {
  return (
    <Field.Root className="br-field" invalid={Boolean(error)} disabled={disabled}>
      <Field.Label className="br-field-label">{label}</Field.Label>
      <Field.Control
        render={
          <Input
            name={name}
            type={type}
            autoComplete={autoComplete}
            value={value}
            readOnly={readOnly}
            required={required}
            placeholder={placeholder}
            dir={dir}
            onChange={(event: ChangeEvent<HTMLInputElement>) => onChange(event.target.value)}
            className="br-field-input"
          />
        }
      />
      {error ? (
        <Field.Error className="br-field-error" match={true}>
          {error}
        </Field.Error>
      ) : null}
    </Field.Root>
  );
}
