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

function poMessages(locale: string): Record<string, string> {
  const text = readFileSync(join(import.meta.dirname, "locales", locale, "messages.po"), "utf8");
  return Object.fromEntries([...text.matchAll(/msgid "([^"]+)"\nmsgstr "((?:\\.|[^"\\])*)"/g)].map((match) => [match[1] ?? "", match[2] ?? ""]));
}

function catalogVariables(message: string): string[] {
  return [...new Set([...message.matchAll(/\{(\w+)(?=[,}])/g)].map((match) => match[1] ?? ""))].sort();
}

function sourceVariables(template: string): string[] {
  let positional = 0;
  const names = [...template.matchAll(/\$\{([^}]*)\}/g)].map((match) => {
    const expression = (match[1] ?? "").trim();
    if (/^[A-Za-z_$][\w$]*$/.test(expression)) {
      return expression;
    }
    const name = String(positional);
    positional += 1;
    return name;
  });
  return [...new Set(names)].sort();
}

describe("placeholders", () => {
  it("TestCatalogPlaceholdersMatchTheSource", () => {
    const catalogs = Object.fromEntries(["en", "fa", "ar"].map((locale) => [locale, poMessages(locale)]));
    const problems: string[] = [];
    let scanned = 0;
    for (const file of sourceFiles(import.meta.dirname)) {
      const source = readFileSync(file, "utf8");
      for (const match of source.matchAll(/\bid:\s*"([a-z][A-Za-z0-9.]*)",\s*message:\s*`([^`]*)`/g)) {
        const id = match[1] ?? "";
        const expected = sourceVariables(match[2] ?? "");
        scanned += expected.length > 0 ? 1 : 0;
        for (const [locale, messages] of Object.entries(catalogs)) {
          const actual = catalogVariables(messages[id] ?? "");
          const plural = (messages[id] ?? "").includes("plural,");
          if (!plural && JSON.stringify(actual) !== JSON.stringify(expected)) {
            problems.push(`${locale} ${id}: source ${expected.join(",")} catalog ${actual.join(",")}`);
          }
        }
      }
    }
    expect(problems).toEqual([]);
    expect(scanned).toBeGreaterThan(5);
  });
});
