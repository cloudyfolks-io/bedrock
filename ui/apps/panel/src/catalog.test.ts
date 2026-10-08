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
