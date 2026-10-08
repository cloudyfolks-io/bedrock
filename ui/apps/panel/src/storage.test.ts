import { afterEach, describe, expect, it, vi } from "vitest";
import { readStored, storedKeys, writeStored } from "./storage";

describe("storage", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    localStorage.clear();
  });

  it("TestOnlyTheDocumentedKeysExist", () => {
    expect(Object.values(storedKeys).sort()).toEqual([
      "bedrock.panel.locale",
      "bedrock.panel.perspective",
      "bedrock.panel.project",
      "bedrock.panel.theme",
    ]);
  });

  it("round trips a value under the documented key", () => {
    writeStored("locale", "fa");
    expect(localStorage.getItem("bedrock.panel.locale")).toBe("fa");
    expect(readStored("locale")).toBe("fa");
    expect(Object.keys(localStorage)).toEqual(["bedrock.panel.locale"]);
  });

  it("returns null and does nothing when storage throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(readStored("theme")).toBeNull();
    expect(() => writeStored("theme", "dark")).not.toThrow();
  });
});
