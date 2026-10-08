import { describe, expect, it } from "vitest";
import { loadCrds } from "../test/crds";
import { renderPanel } from "../test/renderPanel";
import { toFormSchema, type OpenApiDocument } from "./schema";
import { CreateForm, validator } from "./SchemaForm";

describe("real CRDs", () => {
  for (const crd of loadCrds()) {
    for (const locale of ["en", "fa"] as const) {
      it(`TestEveryRealCRDRenders ${crd.kind} in ${locale}`, () => {
        const doc = { components: { schemas: { [`io.cloudyfolks.bedrock.v1alpha1.${crd.kind}`]: crd.schema } } } as OpenApiDocument;
        const { schema, uiSchema } = toFormSchema(doc, crd.kind);
        expect(() => validator.validateFormData({}, schema)).not.toThrow();
        const { container } = renderPanel(<CreateForm schema={schema} uiSchema={uiSchema} onSubmit={() => {}} submitLabel="Create" />, { locale });
        expect(container.querySelectorAll("input, textarea, [role='combobox'], button").length).toBeGreaterThan(0);
      });
    }
  }

  it("renders the free-form values of an Addon as a JSON field", () => {
    const addon = loadCrds().find((crd) => crd.kind === "Addon");
    if (!addon) {
      throw new Error("Addon CRD missing");
    }
    const doc = { components: { schemas: { "io.cloudyfolks.bedrock.v1alpha1.Addon": addon.schema } } } as OpenApiDocument;
    const { schema, uiSchema } = toFormSchema(doc, "Addon");
    const { container } = renderPanel(<CreateForm schema={schema} uiSchema={uiSchema} onSubmit={() => {}} submitLabel="Create" />);
    expect(container.querySelector("textarea.br-code")).toBeTruthy();
  });
});
