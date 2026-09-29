import { describe, expect, it } from "vitest";
import { authRequestFromLocation, loginPath, routeFor, userCodeFromLocation } from "./router";

describe("routeFor", () => {
  it("maps known paths to their route", () => {
    expect(routeFor("/login/device")).toBe("device");
    expect(routeFor("/login/account")).toBe("account");
    expect(routeFor("/login/")).toBe("flow");
    expect(routeFor("/anything/else")).toBe("flow");
  });
});

describe("authRequestFromLocation", () => {
  it("reads the authRequest query parameter", () => {
    expect(authRequestFromLocation("?authRequest=abc123")).toBe("abc123");
    expect(authRequestFromLocation("")).toBeNull();
  });
});

describe("loginPath", () => {
  it("points at the flow entry", () => {
    expect(loginPath()).toBe("/login/");
  });
});

describe("userCodeFromLocation", () => {
  it("normalizes case, dashes and spaces, then regroups by 4", () => {
    expect(userCodeFromLocation("?user_code=wxzb-cdfg")).toBe("WXZB-CDFG");
    expect(userCodeFromLocation("?user_code=wxzb+cdfg")).toBe("WXZB-CDFG");
    expect(userCodeFromLocation("?user_code=WXZBCDFG")).toBe("WXZB-CDFG");
  });

  it("returns an empty string when there is no user_code parameter", () => {
    expect(userCodeFromLocation("")).toBe("");
    expect(userCodeFromLocation("?authRequest=abc123")).toBe("");
  });
});
