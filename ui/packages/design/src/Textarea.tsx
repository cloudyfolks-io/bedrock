import { useId, type ChangeEvent } from "react";

interface TextareaProps {
  label: string;
  value: string;
  onChange: (value: string) => void;
  rows?: number;
  error?: string;
}

function AreaField({ label, value, onChange, rows, error, className, code }: TextareaProps & { className: string; code: boolean }) {
  const id = useId();
  const errorId = useId();
  return (
    <div className="br-field">
      <label htmlFor={id} className="br-field-label">
        {label}
      </label>
      <textarea
        id={id}
        className={className}
        value={value}
        rows={rows ?? 4}
        dir={code ? "ltr" : undefined}
        spellCheck={code ? false : undefined}
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? errorId : undefined}
        onChange={(event: ChangeEvent<HTMLTextAreaElement>) => onChange(event.target.value)}
      />
      {error ? (
        <span id={errorId} className="br-field-error">
          {error}
        </span>
      ) : null}
    </div>
  );
}

export function Textarea(props: TextareaProps) {
  return <AreaField {...props} className="br-textarea" code={false} />;
}

export function CodeTextarea(props: TextareaProps) {
  return <AreaField {...props} className="br-textarea br-code" code={true} />;
}
