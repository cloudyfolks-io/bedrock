import { describe, expect, it } from "vitest";
import { createHttp, isKubeStatusError, type Http } from "./http";
import { conditionOf, isReady, kubeClient, listKey, objectKey, resourcePath, type ResourceRef } from "./kube";
import type { KubeObject } from "./types";

const vms: ResourceRef = { group: "bedrock.cloudyfolks.io", version: "v1alpha1", plural: "virtualmachines", namespaced: true };
const hosts: ResourceRef = { group: "bedrock.cloudyfolks.io", version: "v1alpha1", plural: "hosts", namespaced: false };
const pods: ResourceRef = { group: "", version: "v1", plural: "pods", namespaced: true };

interface Recorded {
  method: string;
  path: string;
  body: unknown;
  contentType: string | undefined;
}

function recorder(reply: { status?: number; body?: unknown } = {}): { http: Http; calls: Recorded[] } {
  const calls: Recorded[] = [];
  const http = createHttp({
    fetch: ((input: string, init: RequestInit) => {
      const headers = init.headers as Record<string, string>;
      calls.push({
        method: String(init.method),
        path: input,
        body: typeof init.body === "string" ? JSON.parse(init.body) : undefined,
        contentType: headers["Content-Type"],
      });
      return Promise.resolve(new Response(JSON.stringify(reply.body ?? {}), { status: reply.status ?? 200 }));
    }) as unknown as typeof fetch,
    csrf: () => "tok",
    onUnauthenticated: () => {},
  });
  return { http, calls };
}

describe("resourcePath", () => {
  it("TestResourcePath", () => {
    expect(resourcePath(pods, { namespace: "web" })).toBe("/api/v1/namespaces/web/pods");
    expect(resourcePath(vms, { namespace: "web", name: "web-1" })).toBe("/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/web/virtualmachines/web-1");
    expect(resourcePath(vms, { namespace: "web", name: "web-1", subresource: "start" })).toBe(
      "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/web/virtualmachines/web-1/start",
    );
    expect(resourcePath(hosts, { name: "host.01" })).toBe("/apis/bedrock.cloudyfolks.io/v1alpha1/hosts/host.01");
    expect(resourcePath(vms, {})).toBe("/apis/bedrock.cloudyfolks.io/v1alpha1/virtualmachines");
    expect(resourcePath(vms, { namespace: "web" }, { labelSelector: "a=b" })).toBe(
      "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/web/virtualmachines?labelSelector=a%3Db",
    );
    expect(() => resourcePath(vms, { namespace: "web", name: "a/b" })).toThrow();
    expect(() => resourcePath(vms, { namespace: "..", name: "a" })).toThrow();
    expect(() => resourcePath(vms, { namespace: "web", name: "..", subresource: "x" })).toThrow();
  });

  it("allows the console subresource with one slash", () => {
    expect(resourcePath(vms, { namespace: "web", name: "web-1", subresource: "console/vnc" })).toContain("/web-1/console/vnc");
  });
});

describe("kubeClient", () => {
  it("TestWritesCarryFieldManager", async () => {
    const { http, calls } = recorder();
    const client = kubeClient(http);
    const object: KubeObject = { metadata: { name: "web-1", namespace: "web", resourceVersion: "7" }, spec: {} };
    await client.create(vms, "web", object);
    await client.replace(vms, object);
    await client.mergePatch(vms, "web", "web-1", { spec: { running: true } });
    await client.remove(vms, "web", "web-1");
    expect(calls[0]).toMatchObject({ method: "POST", path: expect.stringContaining("?fieldManager=bedrock-panel") });
    expect(calls[1]).toMatchObject({ method: "PUT", path: expect.stringContaining("/web-1?fieldManager=bedrock-panel") });
    expect((calls[1]?.body as KubeObject).metadata.resourceVersion).toBe("7");
    expect(calls[2]).toMatchObject({ method: "PATCH", contentType: "application/merge-patch+json" });
    expect(calls[2]?.path).toContain("fieldManager=bedrock-panel");
    expect(calls[3]).toMatchObject({ method: "DELETE" });
    expect(calls[3]?.path).toContain("propagationPolicy=Foreground");
  });

  it("TestPostSubresource", async () => {
    const { http, calls } = recorder({ body: { message: "snapshot started" } });
    const message = await kubeClient(http).postSubresource(vms, "web-ns", "web-1", "start");
    expect(calls[0]).toMatchObject({ method: "POST", path: "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/web-ns/virtualmachines/web-1/start", body: {} });
    expect(message).toBe("snapshot started");
  });

  it("TestConflictIsSurfaced", async () => {
    const { http } = recorder({ status: 409, body: { kind: "Status", reason: "Conflict", message: "changed" } });
    const error = await kubeClient(http)
      .replace(vms, { metadata: { name: "a", namespace: "n" } })
      .catch((reason: unknown) => reason);
    expect(isKubeStatusError(error) && error.reason).toBe("Conflict");
  });

  it("lists with selectors", async () => {
    const { http, calls } = recorder({ body: { items: [], metadata: { resourceVersion: "1" } } });
    await kubeClient(http).list(vms, { namespace: "web", labelSelector: "app=x" });
    expect(calls[0]?.path).toBe("/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/web/virtualmachines?labelSelector=app%3Dx");
  });
});

describe("keys", () => {
  it("TestKeysAreStable", () => {
    expect(listKey(vms, "web", {})).toEqual(listKey(vms, "web", {}));
    expect(listKey(vms, "web", {})).not.toEqual(listKey(vms, "other", {}));
    expect(listKey(vms, "web", {})).not.toEqual(listKey(vms, "web", { labelSelector: "a=b" }));
    expect(objectKey(hosts, undefined, "h1")).toEqual(["kube", "bedrock.cloudyfolks.io", "hosts", "", "object", "h1"]);
  });
});

describe("conditions", () => {
  const object: KubeObject = {
    metadata: { name: "a" },
    status: { conditions: [{ type: "Ready", status: "True" }, { type: "Other", status: "False" }] },
  };

  it("TestConditionHelpers", () => {
    expect(conditionOf(object, "Ready")?.status).toBe("True");
    expect(conditionOf(object, "Missing")).toBeUndefined();
    expect(isReady(object)).toBe(true);
    expect(isReady({ metadata: { name: "b" }, status: { conditions: [{ type: "Ready", status: "Unknown" }] } })).toBe(false);
    expect(isReady({ metadata: { name: "c" } })).toBe(false);
  });
});
