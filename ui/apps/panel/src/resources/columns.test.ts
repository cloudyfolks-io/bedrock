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
