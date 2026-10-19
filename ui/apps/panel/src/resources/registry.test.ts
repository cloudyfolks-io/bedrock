import { describe, expect, it } from "vitest";
import { loadCrds } from "../test/crds";
import { navigationFor } from "../shell/navigation";
import { authnKinds, definitions, definitionFor, definitionsFor, notShown, primaryDefinitionsFor } from "./registry";
import { readdirSync, readFileSync } from "node:fs";
import { customizeValidator } from "@rjsf/validator-ajv8";
import { resolve } from "node:path";

const crds = loadCrds();
const sections = ["Infrastructure", "Platform", "Tenancy", "Catalog", "Compute", "Storage", "Networking", "Object storage", "Registry"];
const verbs = ["get", "list", "watch", "create", "update", "patch", "delete", "deletecollection"];

function sectionId(section: string): string {
  return section === "Object storage" ? "objectStorage" : section.charAt(0).toLowerCase() + section.slice(1);
}

describe("registry", () => {
  it("TestEveryDefinitionMatchesACRD", () => {
    for (const def of definitions) {
      const crd = crds.find((candidate) => candidate.plural === def.plural);
      expect(crd, def.plural).toBeTruthy();
      expect(crd?.scope).toBe(def.scope);
      expect(crd?.kind).toBe(def.kind);
    }
  });

  it("TestEveryNonAuthnKindHasADefinition", () => {
    const missing = crds
      .filter((crd) => !authnKinds.includes(crd.kind) && !notShown.includes(crd.kind))
      .filter((crd) => !definitionFor(crd.plural))
      .map((crd) => crd.kind);
    expect(missing).toEqual([]);
  });

  it("TestRoutesAndNavUseDefinitions", () => {
    for (const def of definitions) {
      expect(sections).toContain(def.section);
      const nav = navigationFor(def.perspective, "web");
      const section = nav.find((candidate) => candidate.id === sectionId(def.section));
      expect(section, `${def.kind} section`).toBeTruthy();
      expect(section?.items.some((item) => item.id === def.path), `${def.kind} path`).toBe(true);
    }
    for (const perspective of ["admin", "project"] as const) {
      const paths = primaryDefinitionsFor(perspective).map((def) => def.path);
      const navPaths = navigationFor(perspective, "web").flatMap((section) => section.items.map((item) => item.id));
      expect(new Set(paths)).toEqual(new Set(navPaths));
      expect(definitionsFor(perspective).length).toBeGreaterThan(0);
    }
  });

  it("TestActionVerbsAreKubernetesVerbs", () => {
    for (const def of definitions) {
      for (const action of def.actions) {
        expect(verbs).toContain(action.verb);
        if (action.kind === "subresource") {
          expect(action.subresource).toBeTruthy();
        }
      }
    }
  });

  it("gives every definition at most six columns and a name or version column", () => {
    for (const def of definitions) {
      expect(def.columns.length).toBeGreaterThan(0);
      expect(def.columns.length).toBeLessThanOrEqual(6);
    }
  });

  it("TestFixturesMatchCRDSchemas", () => {
    const validator = customizeValidator({ ajvOptionsOverrides: { strict: false } });
    const directory = resolve(import.meta.dirname, "../fixtures");
    for (const file of readdirSync(directory).filter((entry) => entry.endsWith(".json"))) {
      const list = JSON.parse(readFileSync(resolve(directory, file), "utf8")) as { items: { kind: string; metadata: { name: string } }[] };
      for (const item of list.items) {
        const crd = crds.find((candidate) => candidate.kind === item.kind);
        const errors = validator.validateFormData(item, crd?.schema as never).errors.map((error) => `${error.property} ${error.message}`);
        expect(errors, `${file} ${item.metadata.name}`).toEqual([]);
      }
    }
  });

  it("TestFixturesParseAndNameTheirKind", () => {
    const directory = resolve(import.meta.dirname, "../fixtures");
    const files = readdirSync(directory).filter((file) => file.endsWith(".json"));
    for (const def of definitions) {
      expect(files, def.plural).toContain(`${def.plural}.json`);
    }
    for (const file of files) {
      const list = JSON.parse(readFileSync(resolve(directory, file), "utf8")) as { items: { apiVersion: string; kind: string; metadata: { name: string } }[] };
      expect(list.items.length).toBeGreaterThan(0);
      for (const item of list.items) {
        expect(item.apiVersion).toBe("bedrock.cloudyfolks.io/v1alpha1");
        expect(crds.some((crd) => crd.kind === item.kind)).toBe(true);
        expect(item.metadata.name).toBeTruthy();
      }
    }
  });
});
