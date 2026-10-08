import type { RJSFSchema, UiSchema } from "@rjsf/utils";

type Node = Record<string, unknown>;

export interface OpenApiDocument {
  components?: { schemas?: Record<string, Node> };
}

export interface FormSchema {
  schema: RJSFSchema;
  uiSchema: UiSchema;
  immutable: string[];
}

interface Converted {
  schema: RJSFSchema;
  ui: UiSchema;
  immutable: string[];
}

const maxDepth = 14;
const refPrefix = "#/components/schemas/";
const copiedKeys = [
  "type",
  "format",
  "description",
  "default",
  "enum",
  "minimum",
  "maximum",
  "exclusiveMinimum",
  "exclusiveMaximum",
  "multipleOf",
  "minLength",
  "maxLength",
  "pattern",
  "minItems",
  "maxItems",
] as const;
const freeForm: RJSFSchema = { type: "object", additionalProperties: true };

function isRecord(value: unknown): value is Node {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function resolve(doc: OpenApiDocument, node: Node, seen: readonly string[]): { node: Node; seen: readonly string[]; cycle: boolean } {
  const ref = node.$ref;
  if (typeof ref !== "string" || !ref.startsWith(refPrefix)) {
    return { node, seen, cycle: false };
  }
  const name = ref.slice(refPrefix.length);
  if (seen.includes(name)) {
    return { node: freeForm as Node, seen, cycle: true };
  }
  const target = doc.components?.schemas?.[name];
  return target ? resolve(doc, target, [...seen, name]) : { node: freeForm as Node, seen, cycle: true };
}

function isImmutable(node: Node): boolean {
  const rules = node["x-kubernetes-validations"];
  return Array.isArray(rules) && rules.some((rule) => isRecord(rule) && typeof rule.rule === "string" && rule.rule.trim() === "self == oldSelf");
}

function copyScalars(node: Node): RJSFSchema {
  const copied: Node = {};
  for (const key of copiedKeys) {
    if (node[key] !== undefined) {
      copied[key] = node[key];
    }
  }
  return copied as RJSFSchema;
}

function withNull(schema: RJSFSchema, node: Node): RJSFSchema {
  return node.nullable === true && typeof schema.type === "string" ? { ...schema, type: [schema.type, "null"] } : schema;
}

function convertProperties(doc: OpenApiDocument, node: Node, path: string[], seen: readonly string[], depth: number): Converted {
  const properties = node.properties as Record<string, Node>;
  const entries = Object.entries(properties).map(([name, child]) => [name, convert(doc, child, [...path, name], seen, depth + 1)] as const);
  const required = Array.isArray(node.required) ? (node.required as string[]).filter((name) => name in properties) : [];
  const schema: RJSFSchema = {
    ...copyScalars(node),
    type: "object",
    properties: Object.fromEntries(entries.map(([name, converted]) => [name, converted.schema])),
    ...(required.length > 0 ? { required } : {}),
  };
  const ui: UiSchema = Object.fromEntries(entries.filter(([, converted]) => Object.keys(converted.ui).length > 0).map(([name, converted]) => [name, converted.ui]));
  return { schema, ui, immutable: entries.flatMap(([, converted]) => converted.immutable) };
}

function convertMap(doc: OpenApiDocument, node: Node, path: string[], seen: readonly string[], depth: number): Converted {
  const value = convert(doc, node.additionalProperties as Node, [...path, "*"], seen, depth + 1);
  return {
    schema: { ...copyScalars(node), type: "object", additionalProperties: value.schema },
    ui: Object.keys(value.ui).length > 0 ? { additionalProperties: value.ui } : {},
    immutable: [],
  };
}

function convertArray(doc: OpenApiDocument, node: Node, path: string[], seen: readonly string[], depth: number): Converted {
  const items = convert(doc, (node.items ?? freeForm) as Node, [...path, "*"], seen, depth + 1);
  const set = node["x-kubernetes-list-type"] === "set";
  return {
    schema: withNull({ ...copyScalars(node), type: "array", items: items.schema, ...(set ? { uniqueItems: true } : {}) }, node),
    ui: Object.keys(items.ui).length > 0 ? { items: items.ui } : {},
    immutable: [],
  };
}

function convert(doc: OpenApiDocument, input: Node, path: string[], seenIn: readonly string[], depth: number): Converted {
  const resolved = resolve(doc, input, seenIn);
  const node = resolved.node;
  const immutable = isImmutable(node) ? [path.join(".")] : [];
  if (depth > maxDepth || resolved.cycle) {
    return { schema: freeForm, ui: { "ui:field": "json" }, immutable };
  }
  if (node["x-kubernetes-int-or-string"] === true) {
    const pattern = typeof node.pattern === "string" ? { pattern: node.pattern } : {};
    return { schema: { type: "string", ...(node.description ? { description: String(node.description) } : {}), ...(node.default !== undefined ? { default: String(node.default) } : {}), ...pattern }, ui: {}, immutable };
  }
  const type = node.type;
  let converted: Converted;
  if (type === "object" && isRecord(node.properties) && Object.keys(node.properties).length > 0) {
    converted = convertProperties(doc, node, path, resolved.seen, depth);
  } else if (type === "object" && isRecord(node.additionalProperties)) {
    converted = convertMap(doc, node, path, resolved.seen, depth);
  } else if (type === "object") {
    converted = { schema: { ...copyScalars(node), type: "object", additionalProperties: true }, ui: { "ui:field": "json" }, immutable: [] };
  } else if (type === "array") {
    converted = convertArray(doc, node, path, resolved.seen, depth);
  } else {
    converted = { schema: withNull(copyScalars(node), node), ui: {}, immutable: [] };
  }
  return { ...converted, immutable: [...immutable, ...converted.immutable] };
}

export function findKindSchema(doc: OpenApiDocument, kind: string): Node | undefined {
  const schemas = doc.components?.schemas ?? {};
  const names = Object.keys(schemas).filter((name) => name.endsWith(`.${kind}`) || name === kind);
  const withSpec = names.map((name) => schemas[name]).find((candidate) => isRecord(candidate) && isRecord(candidate.properties) && "spec" in candidate.properties);
  return withSpec;
}

export function toFormSchema(doc: OpenApiDocument, kind: string): FormSchema {
  const root = findKindSchema(doc, kind);
  const spec = root && isRecord(root.properties) ? (root.properties.spec as Node | undefined) : undefined;
  if (!spec) {
    throw new Error(`no spec schema for ${kind}`);
  }
  const converted = convert(doc, spec, [], [], 0);
  return { schema: converted.schema, uiSchema: converted.ui, immutable: converted.immutable.filter((path) => path !== "") };
}

export function markReadonly(uiSchema: UiSchema, paths: readonly string[]): UiSchema {
  return paths.reduce<UiSchema>((current, path) => setAt(current, path.split("."), { "ui:readonly": true }), uiSchema);
}

function setAt(ui: UiSchema, keys: string[], leaf: UiSchema): UiSchema {
  const [head, ...rest] = keys;
  if (head === undefined) {
    return { ...ui, ...leaf };
  }
  const key = head === "*" ? "items" : head;
  const child = (ui[key] ?? {}) as UiSchema;
  return { ...ui, [key]: setAt(child, rest, leaf) };
}

function withEnum(schema: RJSFSchema, keys: readonly string[], names: readonly string[]): RJSFSchema {
  const [head, ...rest] = keys;
  if (head === undefined) {
    return { ...schema, enum: [...names] };
  }
  if (head === "*") {
    return schema.items && typeof schema.items === "object" && !Array.isArray(schema.items) ? { ...schema, items: withEnum(schema.items as RJSFSchema, rest, names) } : schema;
  }
  const child = schema.properties?.[head];
  return child && typeof child === "object" ? { ...schema, properties: { ...schema.properties, [head]: withEnum(child, rest, names) } } : schema;
}

export function applyRefOptions(schema: RJSFSchema, options: Record<string, readonly string[]>): RJSFSchema {
  return Object.entries(options).reduce((current, [path, names]) => (names.length > 0 ? withEnum(current, path.split("."), names) : current), schema);
}
