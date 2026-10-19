import { describe, expect, it } from "vitest";
import type { KubeObject } from "../api/types";
import { columnsByPlural } from "./columns";

const elasticIp = (spec: Record<string, unknown>, status: Record<string, unknown>): KubeObject =>
  ({ apiVersion: "bedrock.cloudyfolks.io/v1alpha1", kind: "ElasticIP", metadata: { name: "ip1", namespace: "acme-web" }, spec, status }) as KubeObject;

describe("elastic IP columns", () => {
  const column = columnsByPlural.elasticips?.find((def) => def.id === "providerNetwork");

  it("shows the pool that the operator chose", () => {
    expect(column?.sortValue?.(elasticIp({}, { providerNetwork: "pool-b" }))).toBe("pool-b");
  });

  it("shows the named pool before the operator has written the status", () => {
    expect(column?.sortValue?.(elasticIp({ providerNetwork: "external" }, {}))).toBe("external");
  });
});

const container = (status: Record<string, unknown>): KubeObject =>
  ({ apiVersion: "bedrock.cloudyfolks.io/v1alpha1", kind: "Container", metadata: { name: "web", namespace: "acme-web" }, spec: { image: "registry.example.test/web:1" }, status }) as KubeObject;

const build = (spec: Record<string, unknown>, status: Record<string, unknown>): KubeObject =>
  ({ apiVersion: "bedrock.cloudyfolks.io/v1alpha1", kind: "Build", metadata: { name: "shop", namespace: "acme-web" }, spec, status }) as KubeObject;

describe("build columns", () => {
  const columns = columnsByPlural.builds ?? [];

  it("sorts the phase and the type", () => {
    expect(columns.find((def) => def.id === "phase")?.sortValue?.(build({}, { phase: "Succeeded" }))).toBe("Succeeded");
    expect(columns.find((def) => def.id === "type")?.sortValue?.(build({ type: "buildpacks" }, {}))).toBe("buildpacks");
    expect(columns.find((def) => def.id === "type")?.sortValue?.(build({}, {}))).toBe("");
  });

  it("shows the output image", () => {
    const image = columns.find((def) => def.id === "image");
    expect(image?.sortValue?.(build({ output: { image: "registry.example.test/acme/shop:1" } }, {}))).toBe("registry.example.test/acme/shop:1");
  });
});

describe("container columns", () => {
  const columns = columnsByPlural.containers ?? [];

  it("sorts the replicas by the ready count", () => {
    const replicas = columns.find((def) => def.id === "replicas");
    expect(replicas?.sortValue?.(container({ replicas: 3, readyReplicas: 2 }))).toBe(2);
    expect(replicas?.sortValue?.(container({}))).toBe(0);
  });

  it("sorts the addresses by the first pod address", () => {
    const addresses = columns.find((def) => def.id === "addresses");
    expect(addresses?.sortValue?.(container({ addresses: ["10.0.0.4", "10.0.0.5"] }))).toBe("10.0.0.4");
    expect(addresses?.sortValue?.(container({}))).toBe("");
  });

  it("shows the image", () => {
    const image = columns.find((def) => def.id === "image");
    expect(image?.sortValue?.(container({}))).toBe("registry.example.test/web:1");
  });
});
