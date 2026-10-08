import { describe, expect, it } from "vitest";
import { cidrSummary, exposesSsh, isIp, listenerSummary, ruleSummary } from "./summaries";

describe("summaries", () => {
  it("cidrSummary counts addresses", () => {
    expect(cidrSummary("10.100.0.0/24")).toBe("10.100.0.0/24 (256 addresses)");
    expect(cidrSummary("10.0.0.0/8")).toBe("10.0.0.0/8 (2^24 addresses)".replace("2^24", "16777216"));
    expect(cidrSummary("fd00::/64")).toBe("fd00::/64 (2^64 addresses)");
    expect(cidrSummary("nonsense")).toBe("nonsense");
  });

  it("TestRuleSummary", () => {
    expect(ruleSummary({ protocol: "tcp", port: 22, cidr: "10.0.0.0/8" })).toBe("tcp 22 from 10.0.0.0/8");
    expect(ruleSummary({ protocol: "tcp", port: 8000, endPort: 8100, securityGroupRef: "web" })).toBe("tcp 8000-8100 from group web");
    expect(ruleSummary({ cidr: "0.0.0.0/0" })).toBe("all from 0.0.0.0/0");
  });

  it("listenerSummary", () => {
    expect(listenerSummary({ protocol: "TCP", port: 443 })).toBe("TCP 443");
  });

  it("isIp accepts IPv4 and IPv6 only", () => {
    for (const good of ["10.0.0.1", "255.255.255.255", "fd00::5", "2001:db8::1"]) {
      expect(isIp(good), good).toBe(true);
    }
    for (const bad of ["10.0.0", "256.1.1.1", "fd00:::5", "hello", "", "10.0.0.1/24"]) {
      expect(isIp(bad), bad).toBe(false);
    }
  });

  it("exposesSsh finds open rules that cover port 22", () => {
    expect(exposesSsh({ protocol: "tcp", port: 22, cidr: "0.0.0.0/0" })).toBe(true);
    expect(exposesSsh({ protocol: "tcp", port: 20, endPort: 30, cidr: "::/0" })).toBe(true);
    expect(exposesSsh({ protocol: "tcp", port: 443, cidr: "0.0.0.0/0" })).toBe(false);
    expect(exposesSsh({ protocol: "udp", port: 22, cidr: "0.0.0.0/0" })).toBe(false);
    expect(exposesSsh({ protocol: "tcp", port: 22, cidr: "10.0.0.0/8" })).toBe(false);
  });
});
