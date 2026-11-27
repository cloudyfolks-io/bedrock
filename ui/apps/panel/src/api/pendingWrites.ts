import { useSyncExternalStore } from "react";
import type { Http } from "./http";
import type { ResourceRef } from "./kube";
import type { KubeObject } from "./types";

export interface PendingWrite {
  kind: "create" | "update" | "delete" | "action";
  ref: ResourceRef;
  namespace: string | undefined;
  name: string;
  action?: string;
  uid?: string;
  desired?: Record<string, unknown>;
  requested?: true;
}

const empty: readonly PendingWrite[] = [];
const stores = new WeakMap<Http, WriteStore>();

class WriteStore {
  writes: readonly PendingWrite[] = empty;
  reconcile?: (write: PendingWrite) => Promise<void>;
  observedUID?: (write: PendingWrite) => string | undefined;
  reconciliation = Promise.resolve();
  private listeners = new Set<() => void>();

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  };
  snapshot = () => this.writes;

  publish(writes: readonly PendingWrite[]) {
    this.writes = writes;
    for (const listener of this.listeners) listener();
  }
}

function storeOf(http: Http): WriteStore {
  let store = stores.get(http);
  if (!store) {
    store = new WriteStore();
    stores.set(http, store);
  }
  return store;
}

export function configureWrites(
  http: Http,
  reconcile: (write: PendingWrite) => Promise<void>,
  observedUID?: (write: PendingWrite) => string | undefined,
) {
  const store = storeOf(http);
  store.reconcile = reconcile;
  if (observedUID) store.observedUID = observedUID;
}

export function clearPendingWrites(http: Http) {
  const store = storeOf(http);
  store.reconciliation = Promise.resolve();
  store.publish(empty);
}

export function usePendingWrites(http: Http): readonly PendingWrite[] {
  const store = storeOf(http);
  return useSyncExternalStore(store.subscribe, store.snapshot, () => empty);
}

export function sameResource(left: ResourceRef, right: ResourceRef): boolean {
  return left.group === right.group && left.version === right.version && left.plural === right.plural;
}

export function findPendingWrite(writes: readonly PendingWrite[], ref: ResourceRef, object: KubeObject) {
  return writes.find((write) => sameResource(write.ref, ref) && write.name === object.metadata.name &&
    write.namespace === object.metadata.namespace && (!write.uid || write.uid === object.metadata.uid));
}

export async function trackWrite<T>(http: Http, input: PendingWrite, request: () => Promise<T>): Promise<T> {
  const store = storeOf(http);
  if (!store.reconcile) return request();
  const uid = input.uid ?? (input.kind === "create" ? undefined : store.observedUID?.(input));
  const write = uid ? { ...input, uid } : input;
  if (store.writes.some((prior) => sameResource(prior.ref, write.ref) && prior.namespace === write.namespace && prior.name === write.name)) {
    throw new Error("Another change is pending for this resource.");
  }
  const clear = (entry: PendingWrite) => {
    if (store.writes.includes(entry)) store.publish(store.writes.filter((item) => item !== entry));
  };
  store.publish([...store.writes, write]);
  let result: T;
  try {
    result = await request();
  } catch (error) {
    clear(write);
    throw error;
  }
  if (store.writes.includes(write)) {
    const accepted: PendingWrite = { ...write, requested: true };
    store.publish(store.writes.map((entry) => entry === write ? accepted : entry));
    // Later writes must not cancel a refresh already reconciling a visible
    // write to the same list. Each refresh starts after its write was accepted.
    const reconcile = store.reconcile;
    store.reconciliation = store.reconciliation.then(async () => {
      if (!store.writes.includes(accepted)) return;
      try {
        await reconcile(accepted);
      } catch {
        // Keep the last authoritative data when refreshing fails.
      } finally {
        clear(accepted);
      }
    });
  }
  return result;
}

// Project only public desired values. Do not hold credentials, arbitrary
// schema input, or controller status in the optimistic layer.
export function desiredFields(ref: ResourceRef, spec: unknown): Record<string, unknown> | undefined {
  if (typeof spec !== "object" || spec === null || Array.isArray(spec)) return undefined;
  const fields: Record<string, readonly string[]> = {
    addons: ["enabled"],
    hosts: ["maintenance"],
    volumes: ["sizeGi"],
  };
  const values = spec as Record<string, unknown>;
  const projection = Object.fromEntries((fields[ref.plural] ?? []).flatMap((key) => {
    const value = values[key];
    return typeof value === "boolean" || typeof value === "number" ? [[key, value]] : [];
  }));
  return Object.keys(projection).length ? projection : undefined;
}
