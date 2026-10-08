import type { ThemeProps } from "@rjsf/core";
import {
  AddButton,
  ArrayFieldItemButtonsTemplate,
  ArrayFieldItemTemplate,
  ArrayFieldTemplate,
  BaseInputTemplate,
  CheckboxWidget,
  FieldTemplate,
  JsonField,
  ObjectFieldTemplate,
  RemoveButton,
  SelectWidget,
  TextareaWidget,
} from "./widgets";

export const theme: ThemeProps = {
  widgets: { CheckboxWidget, SelectWidget, TextareaWidget },
  fields: { json: JsonField },
  templates: {
    BaseInputTemplate,
    FieldTemplate,
    ObjectFieldTemplate,
    ArrayFieldTemplate,
    ArrayFieldItemTemplate,
    ArrayFieldItemButtonsTemplate,
    ButtonTemplates: { AddButton, RemoveButton } as never,
  },
};
