import { customizeValidator } from "@rjsf/validator-ajv8";
import { describe, expect, it } from "vitest";
import { markReadonly, toFormSchema, type OpenApiDocument } from "./schema";

function doc(spec: unknown, extra: Record<string, unknown> = {}): OpenApiDocument {
  return {
    components: {
      schemas: {
        "io.cloudyfolks.bedrock.v1alpha1.Thing": {
          type: "object",
          properties: { apiVersion: { type: "string" }, kind: { type: "string" }, metadata: { type: "object" }, spec: spec as never, status: { type: "object", properties: { phase: { type: "string" } } } },
        },
        ...extra,
      },
    },
  } as OpenApiDocument;
}

const validator = customizeValidator({ ajvOptionsOverrides: { strict: false } });

describe("toFormSchema", () => {
  it("TestRefsAreResolved", () => {
    const document = doc(
      { $ref: "#/components/schemas/Spec" },
      { Spec: { type: "object", properties: { a: { $ref: "#/components/schemas/Leaf" } } }, Leaf: { type: "string" } },
    );
    expect(toFormSchema(document, "Thing").schema).toEqual({ type: "object", properties: { a: { type: "string" } } });
  });

  it("cuts a reference cycle instead of looping", () => {
    const document = doc(
      { $ref: "#/components/schemas/Node" },
      { Node: { type: "object", properties: { child: { $ref: "#/components/schemas/Node" } } } },
    );
    const { schema } = toFormSchema(document, "Thing");
    expect((schema.properties?.child as { additionalProperties?: boolean }).additionalProperties).toBe(true);
  });

  it("TestStatusAndServerMetadataAreDropped", () => {
    const { schema } = toFormSchema(doc({ type: "object", properties: { size: { type: "integer" } } }), "Thing");
    expect(Object.keys(schema.properties ?? {})).toEqual(["size"]);
    expect(JSON.stringify(schema)).not.toContain("phase");
  });

  it("TestExtensionsAreStripped", () => {
    const quantity = {
      anyOf: [{ type: "integer" }, { type: "string" }],
      pattern: "^(\\+|-)?(([0-9]+(\\.[0-9]*)?)|(\\.[0-9]+))(([KMGTPE]i)|[numkMGTPE]|([eE](\\+|-)?(([0-9]+(\\.[0-9]*)?)|(\\.[0-9]+))))?$",
      "x-kubernetes-int-or-string": true,
    };
    const { schema } = toFormSchema(
      doc({ type: "object", "x-kubernetes-validations": [{ rule: "true" }], properties: { memory: quantity, port: { "x-kubernetes-int-or-string": true }, list: { type: "array", items: { type: "string" }, "x-kubernetes-list-type": "set" } } }),
      "Thing",
    );
    expect(JSON.stringify(schema)).not.toContain("x-kubernetes-");
    const memory = schema.properties?.memory as { type: string };
    expect(memory.type).toBe("string");
    expect((schema.properties?.port as { type: string }).type).toBe("string");
    for (const good of ["4", "500m", "8Gi"]) {
      expect(validator.validateFormData(good, schema.properties?.memory as never).errors, good).toHaveLength(0);
    }
    expect(validator.validateFormData("8 Gi", schema.properties?.memory as never).errors.length).toBeGreaterThan(0);
    expect((schema.properties?.list as { uniqueItems: boolean }).uniqueItems).toBe(true);
  });

  it("TestNullableAndDefaults", () => {
    const { schema } = toFormSchema(doc({ type: "object", properties: { note: { type: "string", nullable: true }, bus: { type: "string", default: "virtio", enum: ["virtio", "sata"] } } }), "Thing");
    expect((schema.properties?.note as { type: unknown }).type).toEqual(["string", "null"]);
    expect((schema.properties?.bus as { default: string }).default).toBe("virtio");
  });

  it("TestImmutableFieldsAreFound", () => {
    const immutableRule = [{ rule: "self == oldSelf" }];
    const { immutable, uiSchema } = toFormSchema(
      doc({ type: "object", properties: { cidr: { type: "string", "x-kubernetes-validations": immutableRule }, network: { type: "object", properties: { vpc: { type: "string", "x-kubernetes-validations": immutableRule } } }, name: { type: "string" } } }),
      "Thing",
    );
    expect(immutable.sort()).toEqual(["cidr", "network.vpc"]);
    expect(JSON.stringify(uiSchema)).not.toContain("ui:readonly");
    const edit = markReadonly(uiSchema, immutable);
    expect((edit.cidr as Record<string, unknown>)["ui:readonly"]).toBe(true);
    expect(((edit.network as Record<string, Record<string, unknown>>).vpc as Record<string, unknown>)["ui:readonly"]).toBe(true);
    expect(edit.name).toBeUndefined();
  });

  it("TestEnumsAndFormats", () => {
    const { schema } = toFormSchema(
      doc({ type: "object", properties: { kind: { type: "string", enum: ["a", "b"] }, at: { type: "string", format: "date-time" }, n: { type: "integer", format: "int32" }, big: { type: "integer", format: "int64" }, d: { type: "number", format: "double" } } }),
      "Thing",
    );
    expect((schema.properties?.kind as { enum: string[] }).enum).toEqual(["a", "b"]);
    expect(() => validator.validateFormData({ n: 1, big: 2, d: 1.5 }, schema)).not.toThrow();
  });

  it("TestArraysOfObjectsAndMaps", () => {
    const { schema, uiSchema } = toFormSchema(
      doc({
        type: "object",
        properties: {
          rules: { type: "array", items: { type: "object", required: ["port", "gone"], properties: { port: { type: "integer" } } } },
          labels: { type: "object", additionalProperties: { type: "string" } },
          values: { type: "object", "x-kubernetes-preserve-unknown-fields": true },
        },
      }),
      "Thing",
    );
    const items = (schema.properties?.rules as { items: { required: string[] } }).items;
    expect(items.required).toEqual(["port"]);
    expect((schema.properties?.labels as { additionalProperties: { type: string } }).additionalProperties.type).toBe("string");
    expect((uiSchema.values as Record<string, unknown>)["ui:field"]).toBe("json");
  });

  it("throws when the kind has no spec", () => {
    expect(() => toFormSchema(doc({ type: "object" }), "Other")).toThrow();
  });
});
