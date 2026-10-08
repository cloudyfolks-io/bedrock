import { useMemo, useState, type ReactNode } from "react";
import { withTheme } from "@rjsf/core";
import { customizeValidator } from "@rjsf/validator-ajv8";
import { englishStringTranslator, type ErrorSchema, type RJSFSchema, type RJSFValidationError, type TranslatableString, type UiSchema } from "@rjsf/utils";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button } from "@bedrock/design";
import { markReadonly } from "./schema";
import { theme } from "./theme";

const ThemedForm = withTheme(theme);
export const validator = customizeValidator({
  ajvOptionsOverrides: { strict: false },
  customFormats: { int32: /^-?\d+$/, int64: /^-?\d+$/, double: /^-?\d+(\.\d+)?$/, byte: /.*/ },
});

interface FormProps {
  schema: RJSFSchema;
  uiSchema: UiSchema;
  formData?: Record<string, unknown> | undefined;
  onSubmit: (spec: Record<string, unknown>) => void;
  extraErrors?: ErrorSchema | undefined;
  formErrors?: string[] | undefined;
  submitLabel: string;
  busy?: boolean | undefined;
  children?: ReactNode;
}

function FormBody({ schema, uiSchema, formData, onSubmit, extraErrors, formErrors, submitLabel, busy, children }: FormProps) {
  const { t } = useLingui();
  const [data, setData] = useState<Record<string, unknown> | undefined>(formData);

  const messages = useMemo<Record<string, string>>(
    () => ({
      required: t({ id: "forms.error.required", message: "This field is required" }),
      pattern: t({ id: "forms.error.pattern", message: "The format is not valid" }),
      minimum: t({ id: "forms.error.minimum", message: "The value is too small" }),
      maximum: t({ id: "forms.error.maximum", message: "The value is too large" }),
      minLength: t({ id: "forms.error.minLength", message: "The text is too short" }),
      maxLength: t({ id: "forms.error.maxLength", message: "The text is too long" }),
      enum: t({ id: "forms.error.enum", message: "Choose one of the options" }),
      type: t({ id: "forms.error.type", message: "The value has the wrong type" }),
      format: t({ id: "forms.error.format", message: "The format is not valid" }),
      minItems: t({ id: "forms.error.minItems", message: "Add more items" }),
      maxItems: t({ id: "forms.error.maxItems", message: "Too many items" }),
      uniqueItems: t({ id: "forms.error.uniqueItems", message: "Items must be different" }),
    }),
    [t],
  );

  return (
    <ThemedForm
      schema={schema}
      uiSchema={uiSchema}
      formData={data}
      validator={validator}
      noHtml5Validate
      showErrorList={false}
      {...(extraErrors ? { extraErrors } : {})}
      experimental_defaultFormStateBehavior={{ emptyObjectFields: "populateRequiredDefaults" }}
      translateString={(key: TranslatableString, params?: string[]) => englishStringTranslator(key, params)}
      transformErrors={(errors: RJSFValidationError[]) => errors.map((error) => ({ ...error, message: messages[error.name ?? ""] ?? error.message ?? "" }))}
      onChange={(event) => setData(event.formData as Record<string, unknown> | undefined)}
      onSubmit={(event) => {
        const { status: _status, ...spec } = (event.formData ?? {}) as Record<string, unknown>;
        onSubmit(spec);
      }}
    >
      {formErrors && formErrors.length > 0 ? (
        <Alert tone="error">
          <ul className="br-form-errors">
            {formErrors.map((message) => (
              <li key={message}>{message}</li>
            ))}
          </ul>
        </Alert>
      ) : null}
      {children}
      <div className="br-dialog-actions">
        <Button variant="primary" type="submit" disabled={busy === true}>
          {submitLabel}
        </Button>
      </div>
    </ThemedForm>
  );
}

export function CreateForm(props: FormProps) {
  return <FormBody {...props} />;
}

export function EditForm({ immutable, uiSchema, ...rest }: FormProps & { immutable: readonly string[] }) {
  return <FormBody {...rest} uiSchema={markReadonly(uiSchema, immutable)} />;
}
