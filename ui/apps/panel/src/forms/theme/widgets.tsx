import { useEffect, useRef, useState } from "react";
import type { FieldProps, ArrayFieldItemButtonsTemplateProps, ArrayFieldItemTemplateProps, ArrayFieldTemplateProps, BaseInputTemplateProps, FieldTemplateProps, IconButtonProps, ObjectFieldTemplateProps, WidgetProps } from "@rjsf/utils";
import { useLingui } from "@lingui/react/macro";
import { Button, Checkbox, CodeTextarea, NumberField, Select, Textarea, TextField } from "@bedrock/design";

const syntheticEvent = { preventDefault: () => {}, stopPropagation: () => {} } as never;
const humanText = /^(displayName|description|title|message|note)/i;
const technicalName = /(cidr|ip|ips|address|url|image|digest|mac|gateway|zone|endpoint|ref|name|key|schedule|class|host|port|id)$/i;

function firstError(props: { rawErrors?: string[] | undefined }): string | undefined {
  return props.rawErrors && props.rawErrors.length > 0 ? props.rawErrors.join(" ") : undefined;
}

function labelOf(props: { label: string; required?: boolean | undefined }): string {
  return props.required ? `${props.label} *` : props.label;
}

export function BaseInputTemplate(props: BaseInputTemplateProps) {
  const { id, value, onChange, label, required, readonly, disabled, placeholder, schema } = props;
  const numeric = schema.type === "integer" || schema.type === "number" || (Array.isArray(schema.type) && (schema.type.includes("integer") || schema.type.includes("number")));
  const error = firstError(props);
  if (numeric) {
    return (
      <NumberField
        label={labelOf({ label, required })}
        value={typeof value === "number" ? value : null}
        onValueChange={(next) => onChange(next === null ? undefined : next)}
        {...(typeof schema.minimum === "number" ? { min: schema.minimum } : {})}
        {...(typeof schema.maximum === "number" ? { max: schema.maximum } : {})}
        {...(schema.type === "integer" ? { step: 1 } : {})}
        {...(error ? { error } : {})}
        readOnly={readonly}
        disabled={disabled}
      />
    );
  }
  return (
    <TextField
      label={labelOf({ label, required })}
      name={id}
      autoComplete="off"
      value={typeof value === "string" ? value : value === undefined || value === null ? "" : String(value)}
      onChange={(next) => onChange(next === "" ? undefined : next)}
      {...(error ? { error } : {})}
      readOnly={readonly}
      disabled={disabled}
      placeholder={placeholder}
      dir={!humanText.test(props.name) && technicalName.test(props.name) ? "ltr" : "auto"}
    />
  );
}

export function TextareaWidget(props: WidgetProps) {
  const error = firstError(props);
  return (
    <Textarea
      label={labelOf(props)}
      value={typeof props.value === "string" ? props.value : ""}
      onChange={(next) => props.onChange(next === "" ? undefined : next)}
      {...(error ? { error } : {})}
      readOnly={props.readonly}
      disabled={props.disabled}
    />
  );
}

export function SelectWidget(props: WidgetProps) {
  const error = firstError(props);
  const options = (props.options.enumOptions ?? []).map((option) => ({ value: String(option.value), label: String(option.label) }));
  return (
    <Select
      label={labelOf(props)}
      value={props.value === undefined || props.value === null ? "" : String(props.value)}
      options={options}
      onValueChange={(next) => props.onChange(next === "" ? undefined : next)}
      {...(error ? { error } : {})}
      disabled={props.disabled || props.readonly}
    />
  );
}

export function CheckboxWidget(props: WidgetProps) {
  return <Checkbox label={labelOf(props)} checked={props.value === true} onCheckedChange={(next) => props.onChange(next)} disabled={props.disabled || props.readonly === true} />;
}

export function JsonField(props: FieldProps) {
  const widgetProps = { ...props, value: props.formData, label: props.name, onChange: (value: unknown) => props.onChange(value, props.fieldPathId.path) } as unknown as WidgetProps;
  return <JsonWidget {...widgetProps} />;
}

export function JsonWidget(props: WidgetProps) {
  const { t } = useLingui();
  const [text, setText] = useState(() => (props.value === undefined ? "" : JSON.stringify(props.value, null, 2)));
  const [parseError, setParseError] = useState<string | undefined>();
  const error = parseError ?? firstError(props);
  return (
    <CodeTextarea
      label={labelOf(props)}
      value={text}
      rows={6}
      onChange={(next) => {
        setText(next);
        if (next.trim() === "") {
          setParseError(undefined);
          props.onChange(undefined);
          return;
        }
        try {
          props.onChange(JSON.parse(next));
          setParseError(undefined);
        } catch {
          setParseError(t({ id: "forms.json.invalid", message: "This is not valid JSON" }));
        }
      }}
      {...(error ? { error } : {})}
      readOnly={props.readonly}
      disabled={props.disabled}
    />
  );
}

export function FieldTemplate(props: FieldTemplateProps) {
  const { children, displayLabel, errors, help, description, hidden } = props;
  if (hidden) {
    return <div hidden>{children}</div>;
  }
  return (
    <div className="br-form-field">
      {displayLabel ? null : description}
      {children}
      {displayLabel ? null : errors}
      {help}
    </div>
  );
}

export function ObjectFieldTemplate(props: ObjectFieldTemplateProps) {
  const { title, description, properties, fieldPathId, registry, uiSchema, schema, readonly, disabled, onAddProperty } = props as ObjectFieldTemplateProps & { onAddProperty?: () => void };
  const { AddButton } = registry.templates.ButtonTemplates;
  const isRoot = fieldPathId.path.length === 0;
  const canAdd = schema.additionalProperties !== undefined && schema.additionalProperties !== false && onAddProperty !== undefined;
  return (
    <fieldset className="br-form-object">
      {isRoot || title === "" ? null : <legend className="br-form-legend">{title}</legend>}
      {description}
      {properties.map((property) => (
        <div key={property.name} className="br-form-property">
          {property.content}
        </div>
      ))}
      {canAdd ? <AddButton onClick={onAddProperty} disabled={disabled || readonly} className="br-form-add" uiSchema={uiSchema ?? {}} registry={registry} /> : null}
    </fieldset>
  );
}

export function ArrayFieldTemplate(props: ArrayFieldTemplateProps) {
  const { title, items, canAdd, onAddClick, registry, uiSchema, disabled, readonly } = props;
  const { AddButton } = registry.templates.ButtonTemplates;
  const container = useRef<HTMLFieldSetElement>(null);
  const previous = useRef(items.length);
  useEffect(() => {
    if (items.length > previous.current) {
      const last = container.current?.querySelectorAll<HTMLElement>(".br-form-array-item");
      last?.[last.length - 1]?.querySelector<HTMLElement>("input, select, textarea, [role='combobox']")?.focus();
    }
    previous.current = items.length;
  }, [items.length]);
  return (
    <fieldset className="br-form-array" ref={container}>
      {title ? <legend className="br-form-legend">{title}</legend> : null}
      {items}
      {canAdd ? <AddButton onClick={onAddClick} disabled={disabled || readonly} className="br-form-add" uiSchema={uiSchema ?? {}} registry={registry} /> : null}
    </fieldset>
  );
}

export function ArrayFieldItemTemplate(props: ArrayFieldItemTemplateProps) {
  const { children, buttonsProps, hasToolbar } = props;
  const { ArrayFieldItemButtonsTemplate } = props.registry.templates;
  return (
    <div className="br-form-array-item">
      <div className="br-form-array-content">{children}</div>
      {hasToolbar ? <ArrayFieldItemButtonsTemplate {...buttonsProps} /> : null}
    </div>
  );
}

export function ArrayFieldItemButtonsTemplate(props: ArrayFieldItemButtonsTemplateProps) {
  const { t } = useLingui();
  const { hasRemove, onRemoveItem, index, disabled, readonly } = props;
  if (!hasRemove) {
    return null;
  }
  return (
    <Button variant="secondary" disabled={disabled || readonly === true} onClick={() => onRemoveItem()}>
      <span aria-hidden="true">−</span>
      <span className="br-visually-hidden">{t({ id: "forms.array.removeItem", message: `Remove item ${index + 1}` })}</span>
    </Button>
  );
}

export function AddButton(props: IconButtonProps) {
  const { t } = useLingui();
  return (
    <Button variant="secondary" disabled={props.disabled === true} onClick={() => props.onClick?.(syntheticEvent)}>
      {t({ id: "forms.array.addItem", message: "Add item" })}
    </Button>
  );
}

export function RemoveButton(props: IconButtonProps) {
  const { t } = useLingui();
  return (
    <Button variant="secondary" disabled={props.disabled === true} onClick={() => props.onClick?.(syntheticEvent)}>
      {t({ id: "forms.array.remove", message: "Remove" })}
    </Button>
  );
}
