import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { RJSFSchema } from "@rjsf/utils";
import { describe, expect, it, vi } from "vitest";
import { renderPanel } from "../test/renderPanel";
import { causesToErrors, splitCauses } from "./errors";
import { CreateForm, EditForm } from "./SchemaForm";

const noUi = {};

function ui(schema: RJSFSchema, overrides: Partial<Parameters<typeof CreateForm>[0]> = {}) {
  return { schema, uiSchema: noUi, onSubmit: () => {}, submitLabel: "Create", ...overrides };
}

describe("SchemaForm", () => {
  it("TestRequiredFieldsShowLocalizedErrors", async () => {
    const user = userEvent.setup();
    renderPanel(<CreateForm {...ui({ type: "object", required: ["networkRef"], properties: { networkRef: { type: "string" } } })} />);
    await user.click(screen.getByRole("button", { name: "Create" }));
    const input = await screen.findByLabelText(/networkRef/);
    expect(await screen.findByText("This field is required")).toBeTruthy();
    expect(input.getAttribute("aria-invalid")).toBe("true");
    const describedBy = input.getAttribute("aria-describedby") ?? "";
    expect(describedBy.split(" ").some((id) => document.getElementById(id)?.textContent === "This field is required")).toBe(true);
  });

  it("TestSubmitGivesTheValidSpec", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    const schema: RJSFSchema = {
      type: "object",
      properties: { sizeGi: { type: "integer" }, memory: { type: "string", pattern: "^[0-9]+(Gi|Mi)?$" }, running: { type: "boolean" } },
    };
    renderPanel(<CreateForm {...ui(schema, { onSubmit })} />);
    await user.type(screen.getByRole("textbox", { name: "sizeGi" }), "4");
    await user.type(screen.getByLabelText("memory"), "8Gi");
    await user.click(screen.getByRole("checkbox", { name: "running" }));
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    expect(onSubmit.mock.calls[0]?.[0]).toEqual({ sizeGi: 4, memory: "8Gi", running: true });
    expect(onSubmit.mock.calls[0]?.[0]).not.toHaveProperty("status");
  });

  it("TestEditMakesImmutableFieldsReadOnly", () => {
    const schema: RJSFSchema = { type: "object", properties: { cidr: { type: "string" } } };
    const edit = renderPanel(<EditForm {...ui(schema, { formData: { cidr: "10.0.0.0/24" } })} immutable={["cidr"]} />);
    expect((screen.getByLabelText("cidr") as HTMLInputElement).readOnly).toBe(true);
    edit.unmount();
    renderPanel(<CreateForm {...ui(schema, { formData: { cidr: "10.0.0.0/24" } })} />);
    expect((screen.getByLabelText("cidr") as HTMLInputElement).readOnly).toBe(false);
  });

  it("TestArrayEditorIsAccessible", async () => {
    const user = userEvent.setup();
    const schema: RJSFSchema = { type: "object", properties: { sshKeys: { type: "array", items: { type: "string" } } } };
    renderPanel(<CreateForm {...ui(schema, { formData: { sshKeys: ["a"] } })} />);
    await user.click(screen.getByRole("button", { name: "Add item" }));
    expect(screen.getByRole("button", { name: "Remove item 2" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Remove item 1" })).toBeTruthy();
    await waitFor(() => {
      const inputs = screen.getAllByRole("textbox");
      expect(document.activeElement).toBe(inputs[inputs.length - 1]);
    });
    await user.click(screen.getByRole("button", { name: "Remove item 2" }));
    expect(screen.queryByRole("button", { name: "Remove item 2" })).toBeNull();
  });

  it("TestServerCausesLandOnFields", () => {
    const schema: RJSFSchema = { type: "object", properties: { cidr: { type: "string" }, networks: { type: "array", items: { type: "object", properties: { subnetRef: { type: "string" } } } } } };
    renderPanel(<CreateForm {...ui(schema, { extraErrors: causesToErrors([{ field: "spec.cidr", message: "overlaps" }]), formErrors: ["spec.zzz: unknown"] })} />);
    expect(screen.getByText("overlaps")).toBeTruthy();
    expect(screen.getByText("spec.zzz: unknown")).toBeTruthy();
    expect(causesToErrors([{ field: "spec.networks[0].subnetRef", message: "gone" }])).toEqual({ networks: { "0": { subnetRef: { __errors: ["gone"] } } } });
    const { mapped, unmapped } = splitCauses(
      [
        { field: "spec.cidr", message: "a" },
        { field: "spec.networks[0].subnetRef", message: "b" },
        { field: "spec.nothing", message: "c" },
      ],
      schema,
    );
    expect(mapped).toHaveLength(2);
    expect(unmapped).toHaveLength(1);
  });

  it("TestRtlForm", () => {
    const schema: RJSFSchema = { type: "object", properties: { cidr: { type: "string" }, displayName: { type: "string" } } };
    renderPanel(<CreateForm {...ui(schema)} />, { locale: "fa" });
    expect(document.documentElement.dir).toBe("rtl");
    expect(screen.getByLabelText("cidr").getAttribute("dir")).toBe("ltr");
    expect(screen.getByLabelText("displayName").getAttribute("dir")).toBe("auto");
    expect(document.querySelector(".br-field-label")).toBeTruthy();
  });

  it("TestWidgetsUseTheDesignSystem", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: { name: { type: "string" }, count: { type: "integer" }, bus: { type: "string", enum: ["virtio", "sata"] }, on: { type: "boolean" }, notes: { type: "string" } },
    };
    const { container } = renderPanel(<CreateForm {...ui(schema, { uiSchema: { notes: { "ui:widget": "textarea" } } })} />);
    for (const selector of [".br-field-input", ".br-number", ".br-select-trigger", ".br-checkbox", ".br-textarea"]) {
      expect(container.querySelector(selector), selector).toBeTruthy();
    }
  });

  it("edits free-form objects through the JSON widget", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    const schema: RJSFSchema = { type: "object", properties: { values: { type: "object", additionalProperties: true } } };
    renderPanel(<CreateForm {...ui(schema, { uiSchema: { values: { "ui:field": "json" } }, onSubmit })} />);
    const area = screen.getByLabelText("values");
    await user.click(area);
    await user.paste("{bad");
    expect(await screen.findByText("This is not valid JSON")).toBeTruthy();
    await user.clear(area);
    await user.click(area);
    await user.paste('{"a": 1}');
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith({ values: { a: 1 } }));
  });
});
