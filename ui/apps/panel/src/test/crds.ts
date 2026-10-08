import { readdirSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { parse } from "yaml";

export interface Crd {
  kind: string;
  plural: string;
  scope: "Namespaced" | "Cluster";
  schema: Record<string, unknown>;
}

const crdDirectory = resolve(import.meta.dirname, "../../../../../manifests/00-crds");

export function loadCrds(): Crd[] {
  return readdirSync(crdDirectory)
    .filter((file) => file.startsWith("bedrock.cloudyfolks.io_") && file.endsWith(".yaml"))
    .map((file) => {
      const doc = parse(readFileSync(resolve(crdDirectory, file), "utf8")) as {
        spec: { names: { kind: string; plural: string }; scope: "Namespaced" | "Cluster"; versions: { schema: { openAPIV3Schema: Record<string, unknown> } }[] };
      };
      return { kind: doc.spec.names.kind, plural: doc.spec.names.plural, scope: doc.spec.scope, schema: doc.spec.versions[0]?.schema.openAPIV3Schema ?? {} };
    });
}
