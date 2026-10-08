import type { ErrorSchema, RJSFSchema } from "@rjsf/utils";

export interface Cause {
  field: string;
  message: string;
}

function segmentsOf(field: string): string[] {
  const parts = field
    .replace(/\[(\d+)\]/g, ".$1")
    .split(".")
    .filter((part) => part !== "");
  return parts[0] === "spec" ? parts.slice(1) : parts;
}

function addError(tree: ErrorSchema, segments: string[], message: string): ErrorSchema {
  const [head, ...rest] = segments;
  if (head === undefined) {
    const current = (tree as { __errors?: string[] }).__errors ?? [];
    return { ...tree, __errors: [...current, message] } as ErrorSchema;
  }
  const child = ((tree as Record<string, ErrorSchema>)[head] ?? {}) as ErrorSchema;
  return { ...tree, [head]: addError(child, rest, message) } as ErrorSchema;
}

export function causesToErrors(causes: readonly Cause[]): ErrorSchema {
  return causes.reduce<ErrorSchema>((tree, cause) => addError(tree, segmentsOf(cause.field), cause.message), {} as ErrorSchema);
}

function hasPath(schema: RJSFSchema | undefined, segments: readonly string[]): boolean {
  const [head, ...rest] = segments;
  if (head === undefined) {
    return schema !== undefined;
  }
  if (!schema) {
    return false;
  }
  if (schema.type === "array" || (Array.isArray(schema.type) && schema.type.includes("array"))) {
    return /^\d+$/.test(head) && hasPath(schema.items as RJSFSchema | undefined, rest);
  }
  const named = schema.properties?.[head];
  if (named !== undefined && typeof named !== "boolean") {
    return hasPath(named, rest);
  }
  const additional = schema.additionalProperties;
  return additional === true ? true : additional !== undefined && additional !== false && hasPath(additional as RJSFSchema, rest);
}

export function splitCauses(causes: readonly Cause[], schema: RJSFSchema): { mapped: Cause[]; unmapped: Cause[] } {
  const mapped = causes.filter((cause) => hasPath(schema, segmentsOf(cause.field)));
  return { mapped, unmapped: causes.filter((cause) => !mapped.includes(cause)) };
}
