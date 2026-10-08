import { lazy, Suspense } from "react";
import type { KubeObject } from "../api/types";
import type { ResourceDef } from "../resources/registry";

const Create = lazy(() => import("./ResourceFormDialog").then((module) => ({ default: module.CreateResourceDialog })));
const Edit = lazy(() => import("./ResourceFormDialog").then((module) => ({ default: module.EditResourceDialog })));

export function LazyCreateDialog(props: { def: ResourceDef; onClose: () => void; initialSpec?: Record<string, unknown> }) {
  return (
    <Suspense fallback={null}>
      <Create {...props} />
    </Suspense>
  );
}

export function LazyEditDialog(props: { def: ResourceDef; object: KubeObject; onClose: () => void }) {
  return (
    <Suspense fallback={null}>
      <Edit {...props} />
    </Suspense>
  );
}
