import { afterEach, describe, expect, it, vi } from "vitest";
import { readSession, readStored, removeSession, sessionKeys, storedKeys, writeSession, writeStored } from "./storage";

describe("storage", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    localStorage.clear();
    sessionStorage.clear();
  });

  it("TestOnlyTheDocumentedKeysExist", () => {
    expect(Object.values(storedKeys).sort()).toEqual([
      "bedrock.panel.locale",
      "bedrock.panel.perspective",
      "bedrock.panel.project",
      "bedrock.panel.theme",
    ]);
  });

  it("keeps one session key for the sign-in guard", () => {
    expect(Object.values(sessionKeys)).toEqual(["bedrock.panel.signin"]);
    writeSession("signin", "1");
    expect(readSession("signin")).toBe("1");
    removeSession("signin");
    expect(readSession("signin")).toBeNull();
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
