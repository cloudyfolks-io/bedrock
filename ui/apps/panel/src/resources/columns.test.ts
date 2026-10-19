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

const database = (spec: Record<string, unknown>, status: Record<string, unknown>): KubeObject =>
  ({ apiVersion: "bedrock.cloudyfolks.io/v1alpha1", kind: "Database", metadata: { name: "orders", namespace: "acme-web" }, spec, status }) as KubeObject;

describe("database columns", () => {
  const columns = columnsByPlural.databases ?? [];

  it("sorts by the phase and the first proxy address", () => {
    expect(columns.find((def) => def.id === "phase")?.sortValue?.(database({}, { phase: "Ready" }))).toBe("Ready");
    const addresses = columns.find((def) => def.id === "addresses");
    expect(addresses?.sortValue?.(database({}, { addresses: [{ subnet: "default", ip: "10.100.0.20", port: 5432 }] }))).toBe("10.100.0.20");
    expect(addresses?.sortValue?.(database({}, {}))).toBe("");
  });

  it("shows the engine, the version and the class", () => {
    const spec = { engine: "postgresql", version: "17", class: "small" };
    for (const [id, want] of [["engine", "postgresql"], ["version", "17"], ["class", "small"]] as const) {
      expect(columns.find((def) => def.id === id)?.sortValue?.(database(spec, {}))).toBe(want);
    }
  });
});

describe("database access columns", () => {
  const user = (spec: Record<string, unknown>, status: Record<string, unknown>): KubeObject =>
    ({ apiVersion: "bedrock.cloudyfolks.io/v1alpha1", kind: "DatabaseUser", metadata: { name: "alice", namespace: "acme-web" }, spec, status }) as KubeObject;

  it("counts the grants of a user", () => {
    const grants = columnsByPlural.databaseusers?.find((def) => def.id === "grants");
    expect(grants?.sortValue?.(user({ grants: [{ database: "app", level: "all" }, { database: "shop", level: "readonly" }] }, {}))).toBe(2);
    expect(grants?.sortValue?.(user({}, {}))).toBe(0);
  });

  it("shows the real database name of a schema", () => {
    const schema = { apiVersion: "bedrock.cloudyfolks.io/v1alpha1", kind: "DatabaseSchema", metadata: { name: "scratch", namespace: "acme-web" }, spec: {}, status: { name: "scratch_data" } } as KubeObject;
    expect(columnsByPlural.databaseschemas?.find((def) => def.id === "databaseName")?.sortValue?.(schema)).toBe("scratch_data");
  });
});
