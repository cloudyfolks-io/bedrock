import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { messages as ar } from "./locales/ar/messages.po";
import { messages as en } from "./locales/en/messages.po";
import { messages as fa } from "./locales/fa/messages.po";

const idPattern = /^[a-z][A-Za-z0-9]*(\.[a-z][A-Za-z0-9]*)+$/;

function isEmpty(value: unknown): boolean {
  return value === "" || (Array.isArray(value) && value.length === 0) || value === undefined || value === null;
}

describe("catalogs", () => {
  it("TestCatalogsAreComplete", () => {
    const ids = Object.keys(en).sort();
    expect(Object.keys(fa).sort()).toEqual(ids);
    expect(Object.keys(ar).sort()).toEqual(ids);
    for (const id of ids) {
      expect(idPattern.test(id), id).toBe(true);
      for (const catalog of [fa, ar]) {
        expect(isEmpty(catalog[id]), `${id} is empty`).toBe(false);
        expect(catalog[id], `${id} equals its id`).not.toBe(id);
      }
    }
  });
});

function sourceFiles(directory: string): string[] {
  return readdirSync(directory).flatMap((entry) => {
    const path = join(directory, entry);
    if (statSync(path).isDirectory()) {
      return entry === "locales" || entry === "fixtures" ? [] : sourceFiles(path);
    }
    return /\.(ts|tsx)$/.test(entry) && !/\.test\.tsx?$/.test(entry) ? [path] : [];
  });
}

describe("source messages", () => {
  it("TestEverySourceMessageHasACatalogEntry", () => {
    const used = new Set<string>();
    for (const file of sourceFiles(import.meta.dirname)) {
      for (const match of readFileSync(file, "utf8").matchAll(/\bid:\s*"([a-z][A-Za-z0-9]*(?:\.[a-z][A-Za-z0-9]*)+)"/g)) {
        used.add(match[1] ?? "");
      }
    }
    const missing = [...used].filter((id) => !(id in en));
    expect(missing).toEqual([]);
    expect(used.size).toBeGreaterThan(50);
  });
});
