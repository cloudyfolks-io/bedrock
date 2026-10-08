export interface ObjectMeta {
  name: string;
  namespace?: string;
  uid?: string;
  resourceVersion?: string;
  generation?: number;
  creationTimestamp?: string;
  deletionTimestamp?: string;
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
}

export interface Condition {
  type: string;
  status: "True" | "False" | "Unknown";
  reason?: string;
  message?: string;
  lastTransitionTime?: string;
  observedGeneration?: number;
}

export interface KubeObject {
  apiVersion?: string;
  kind?: string;
  metadata: ObjectMeta;
  spec?: Record<string, unknown>;
  status?: Record<string, unknown>;
}

export interface KubeList<T extends KubeObject = KubeObject> {
  apiVersion?: string;
  kind?: string;
  metadata: { resourceVersion?: string };
  items: T[];
}

export interface SessionUser {
  name: string;
  displayName: string;
  email: string;
  groups: string[];
}

export interface Session {
  user: SessionUser;
  admin: boolean;
  csrf: string;
  expiresAt: string;
  host: string;
}

export interface Check {
  verb: string;
  group?: string;
  resource: string;
  subresource?: string;
  namespace?: string;
  name?: string;
}
