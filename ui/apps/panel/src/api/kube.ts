import type { Http } from "./http";
import type { Condition, KubeList, KubeObject } from "./types";

export interface ResourceRef {
  group: string;
  version: string;
  plural: string;
  namespaced: boolean;
}

export interface Target {
  namespace?: string;
  name?: string;
  subresource?: string;
}

export interface Selectors {
  labelSelector?: string;
  fieldSelector?: string;
}

const fieldManager = "bedrock-panel";
const merge = "application/merge-patch+json";

function segment(value: string): string {
  if (value === "" || value === "." || value === ".." || value.includes("/")) {
    throw new Error(`invalid path segment: ${value}`);
  }
  return encodeURIComponent(value);
}

function queryString(query: Record<string, string> | undefined): string {
  const entries = Object.entries(query ?? {});
  return entries.length === 0 ? "" : `?${entries.map(([key, value]) => `${key}=${encodeURIComponent(value)}`).join("&")}`;
}

function base(ref: ResourceRef): string {
  return ref.group === "" ? `/api/${ref.version}` : `/apis/${ref.group}/${ref.version}`;
}

export function resourcePath(ref: ResourceRef, target: Target, query?: Record<string, string>): string {
  const namespace = ref.namespaced && target.namespace !== undefined ? `/namespaces/${segment(target.namespace)}` : "";
  const name = target.name !== undefined ? `/${segment(target.name)}` : "";
  const subresource = target.subresource !== undefined ? `/${target.subresource.split("/").map(segment).join("/")}` : "";
  return `${base(ref)}${namespace}/${ref.plural}${name}${subresource}${queryString(query)}`;
}

function selectorQuery(selectors: Selectors): Record<string, string> {
  return {
    ...(selectors.labelSelector ? { labelSelector: selectors.labelSelector } : {}),
    ...(selectors.fieldSelector ? { fieldSelector: selectors.fieldSelector } : {}),
  };
}

export interface KubeClient {
  list<T extends KubeObject = KubeObject>(ref: ResourceRef, scope: { namespace?: string } & Selectors, signal?: AbortSignal): Promise<KubeList<T>>;
  get<T extends KubeObject = KubeObject>(ref: ResourceRef, namespace: string | undefined, name: string): Promise<T>;
  create<T extends KubeObject = KubeObject>(ref: ResourceRef, namespace: string | undefined, object: T): Promise<T>;
  replace<T extends KubeObject = KubeObject>(ref: ResourceRef, object: T): Promise<T>;
  mergePatch<T extends KubeObject = KubeObject>(ref: ResourceRef, namespace: string | undefined, name: string, patch: unknown): Promise<T>;
  remove(ref: ResourceRef, namespace: string | undefined, name: string): Promise<void>;
  postSubresource(ref: ResourceRef, namespace: string | undefined, name: string, action: string): Promise<string | undefined>;
}

export function kubeClient(http: Http): KubeClient {
  return {
    list: (ref, scope, signal) =>
      http.request("GET", resourcePath(ref, scope.namespace === undefined ? {} : { namespace: scope.namespace }, selectorQuery(scope)), signal ? { signal } : {}),
    get: (ref, namespace, name) => http.request("GET", resourcePath(ref, withName(namespace, name))),
    create: (ref, namespace, object) =>
      http.request("POST", resourcePath(ref, namespace === undefined ? {} : { namespace }, { fieldManager }), { body: object }),
    replace: (ref, object) =>
      http.request("PUT", resourcePath(ref, withName(object.metadata.namespace, object.metadata.name), { fieldManager }), { body: object }),
    mergePatch: (ref, namespace, name, patch) =>
      http.request("PATCH", resourcePath(ref, withName(namespace, name), { fieldManager }), { body: patch, contentType: merge }),
    remove: (ref, namespace, name) => http.request("DELETE", resourcePath(ref, withName(namespace, name), { propagationPolicy: "Foreground" })),
    postSubresource: async (ref, namespace, name, action) => {
      const answer = await http.request<{ message?: string } | undefined>("POST", resourcePath(ref, { ...withName(namespace, name), subresource: action }), { body: {} });
      return answer?.message;
    },
  };
}

function withName(namespace: string | undefined, name: string): Target {
  return namespace === undefined ? { name } : { namespace, name };
}

export function listKey(ref: ResourceRef, namespace: string | undefined, selectors: Selectors): readonly unknown[] {
  return ["kube", ref.group, ref.plural, namespace ?? "", "list", `${selectors.labelSelector ?? ""}|${selectors.fieldSelector ?? ""}`];
}

export function objectKey(ref: ResourceRef, namespace: string | undefined, name: string): readonly unknown[] {
  return ["kube", ref.group, ref.plural, namespace ?? "", "object", name];
}

export function conditionOf(object: KubeObject, type: string): Condition | undefined {
  const conditions = object.status?.conditions;
  return Array.isArray(conditions) ? (conditions as Condition[]).find((condition) => condition.type === type) : undefined;
}

export function isReady(object: KubeObject): boolean {
  return conditionOf(object, "Ready")?.status === "True";
}
