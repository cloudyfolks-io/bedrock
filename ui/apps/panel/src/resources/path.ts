import type { KubeObject } from "../api/types";

export function valueAt(object: unknown, path: string): unknown {
  return path.split(".").reduce<unknown>((current, key) => (current !== null && typeof current === "object" ? (current as Record<string, unknown>)[key] : undefined), object);
}

export function textAt(object: KubeObject, path: string): string {
  const value = valueAt(object, path);
  return typeof value === "string" || typeof value === "number" || typeof value === "boolean" ? String(value) : "";
}

export function listAt(object: KubeObject, path: string): unknown[] {
  const value = valueAt(object, path);
  return Array.isArray(value) ? value : [];
}
