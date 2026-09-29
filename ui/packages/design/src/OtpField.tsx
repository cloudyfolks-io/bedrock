import { useRef } from "react";
import type { ChangeEvent, ClipboardEvent, KeyboardEvent } from "react";

interface OtpFieldProps {
  length: number;
  value: string;
  onChange: (value: string) => void;
  label: string;
}

export function OtpField({ length, value, onChange, label }: OtpFieldProps) {
  const inputs = useRef<Array<HTMLInputElement | null>>([]);
  const digits = Array.from({ length }, (_, index) => value[index] ?? "");

  const setDigit = (index: number, digit: string) => {
    const next = digits.slice();
    next[index] = digit;
    onChange(next.join(""));
  };

  const handleChange = (index: number) => (event: ChangeEvent<HTMLInputElement>) => {
    const raw = event.target.value;
    if (!raw) {
      setDigit(index, "");
      return;
    }
    const digit = raw.replace(/\D/g, "").slice(-1);
    if (!digit) {
      return;
    }
    setDigit(index, digit);
    if (index < length - 1) {
      inputs.current[index + 1]?.focus();
    }
  };

  const handleKeyDown = (index: number) => (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Backspace" && !digits[index] && index > 0) {
      setDigit(index - 1, "");
      inputs.current[index - 1]?.focus();
    }
  };

  const handlePaste = (event: ClipboardEvent<HTMLInputElement>) => {
    const pasted = event.clipboardData.getData("text").replace(/\D/g, "");
    if (!pasted) {
      return;
    }
    event.preventDefault();
    onChange(pasted.slice(0, length));
    inputs.current[Math.min(pasted.length, length) - 1]?.focus();
  };

  return (
    <fieldset className="br-otp">
      <legend className="br-field-label">{label}</legend>
      {digits.map((digit, index) => (
        <input
          key={index}
          ref={(element) => {
            inputs.current[index] = element;
          }}
          className="br-otp-digit"
          inputMode="numeric"
          pattern="[0-9]*"
          maxLength={1}
          value={digit}
          onChange={handleChange(index)}
          onKeyDown={handleKeyDown(index)}
          onPaste={handlePaste}
          aria-label={`${label} ${index + 1}`}
        />
      ))}
    </fieldset>
  );
}
