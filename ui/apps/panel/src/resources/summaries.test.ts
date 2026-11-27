import { describe, expect, it } from "vitest";
import { cidrSummary, exposesSsh, isIp, listenerSummary, loadBalancerElasticIPOptions, networkCidrs, rulePorts, shortImageDigest } from "./summaries";

describe("summaries", () => {
  it("cidrSummary counts addresses", () => {
    expect(cidrSummary("10.100.0.0/24")).toBe("10.100.0.0/24 (256 addresses)");
    expect(cidrSummary("10.0.0.0/8")).toBe("10.0.0.0/8 (2^24 addresses)".replace("2^24", "16777216"));
    expect(cidrSummary("fd00::/64")).toBe("fd00::/64 (2^64 addresses)");
    expect(cidrSummary("nonsense")).toBe("nonsense");
  });

  it("networkCidrs lists every range and reads an old cidr as one range", () => {
    const network = (spec: Record<string, unknown>) => ({ apiVersion: "bedrock.cloudyfolks.io/v1alpha1", kind: "Network", metadata: { name: "acme" }, spec });
    expect(networkCidrs(network({ cidrs: ["10.0.0.0/16", "172.16.0.0/16"] }))).toBe("10.0.0.0/16, 172.16.0.0/16");
    expect(networkCidrs(network({ cidr: "10.100.0.0/16" }))).toBe("10.100.0.0/16");
    expect(networkCidrs(network({}))).toBe("");
  });

  it("shows single, ranged and unrestricted ports", () => {
    expect(rulePorts({ port: 22 })).toBe("22");
    expect(rulePorts({ port: 8000, endPort: 8100 })).toBe("8000-8100");
    expect(rulePorts({})).toBe("—");
  });

  it("shortens only a full SHA-256 image digest", () => {
    const image = "registry.test/postgres:17";
    expect(shortImageDigest(`${image}@sha256:${"0123456789ab".padEnd(64, "d")}`)).toBe(`${image}@sha256:0123456789ab…`);
    expect(shortImageDigest(image)).toBe(image);
    expect(shortImageDigest(`${image}@sha256:abcd`)).toBe(`${image}@sha256:abcd`);
  });

  it("offers free and same-load-balancer addresses while preserving the saved reference", () => {
    const items = [
      { metadata: { name: "reserved" } },
      { metadata: { name: "attached" }, spec: { target: { kind: "LoadBalancer", name: "web" } } },
      { metadata: { name: "other-lb" }, spec: { target: { kind: "LoadBalancer", name: "other" } } },
      { metadata: { name: "vm" }, spec: { target: { kind: "VirtualMachine", name: "web" } } },
      { metadata: { name: "deleting", deletionTimestamp: "2026-11-27" } },
    ];
    expect(loadBalancerElasticIPOptions(items, "web", "missing")).toEqual(["", "reserved", "attached", "missing"]);
    expect(loadBalancerElasticIPOptions(items, "", "")).toEqual(["", "reserved"]);
    expect(loadBalancerElasticIPOptions([], "web", "existing")).toEqual(["", "existing"]);
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
