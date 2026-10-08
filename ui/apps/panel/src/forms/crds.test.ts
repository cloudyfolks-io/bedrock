import { customizeValidator } from "@rjsf/validator-ajv8";
import { describe, expect, it } from "vitest";
import { loadCrds } from "../test/crds";
import { toFormSchema, type OpenApiDocument } from "./schema";

const validator = customizeValidator({ ajvOptionsOverrides: { strict: false }, customFormats: { int32: /^-?\d+$/, int64: /^-?\d+$/, double: /^-?\d+(\.\d+)?$/, byte: /.*/ } });

describe("real CRDs", () => {
  it("TestEveryRealCRDConverts", () => {
    for (const crd of loadCrds()) {
      const doc = { components: { schemas: { [`io.cloudyfolks.bedrock.v1alpha1.${crd.kind}`]: crd.schema } } } as OpenApiDocument;
      const { schema } = toFormSchema(doc, crd.kind);
      expect(() => validator.validateFormData({}, schema), crd.kind).not.toThrow();
    }
  });
});
