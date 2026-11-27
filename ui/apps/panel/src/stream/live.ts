import { useEffect, useState } from "react";
import { useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import { useHttp } from "../api/HttpProvider";
import { isHttpError } from "../api/http";
import { kubeClient, listKey, objectKey, resourcePath, type ResourceRef } from "../api/kube";
import { findPendingWrite, sameResource, usePendingWrites, type PendingWrite } from "../api/pendingWrites";
import type { KubeList, KubeObject } from "../api/types";
import { useMultiplexer } from "./StreamProvider";
import type { Multiplexer } from "./multiplexer";
import { bedrockGroup, type BookmarkFrame, type EventFrame, type Subscription } from "./protocol";

export type LiveState = "connecting" | "live" | "paused";

interface QueryWatch {
  state: LiveState;
  listeners: Set<(state: LiveState) => void>;
  dispose: () => void;
}

const listStaleMs = 30_000;
const objectWatchVersions = new WeakMap<object, string>();
const queryWatches = new WeakMap<object, Map<Multiplexer, QueryWatch>>();

function identity(object: KubeObject): string {
  return object.metadata.uid ?? `${object.metadata.namespace ?? ""}/${object.metadata.name}`;
}

function byName(left: KubeObject, right: KubeObject): number {
  return left.metadata.name.localeCompare(right.metadata.name) || (left.metadata.namespace ?? "").localeCompare(right.metadata.namespace ?? "");
}

function applyDesired<T extends KubeObject>(object: T, writes: readonly PendingWrite[], ref: ResourceRef): T {
  const desired = findPendingWrite(writes, ref, object)?.desired;
  return desired ? { ...object, spec: { ...object.spec, ...desired } } : object;
}

function projectList<T extends KubeObject>(
  data: KubeList<T> | undefined,
  writes: readonly PendingWrite[],
  ref: ResourceRef,
  scope: { namespace?: string; labelSelector?: string; listPath?: string; optimisticCreates?: boolean },
): KubeList<T> | undefined {
  if (!data || !writes.some((write) => sameResource(write.ref, ref))) return data;
  const items = data.items.map((item) => applyDesired(item, writes, ref));
  if (scope.optimisticCreates && !scope.labelSelector && !scope.listPath) {
    for (const write of writes) {
      if (write.kind !== "create" || !sameResource(write.ref, ref) ||
        (ref.namespaced && scope.namespace !== undefined && scope.namespace !== write.namespace) ||
        (ref.namespaced && write.namespace === undefined) ||
        items.some((item) => item.metadata.name === write.name && item.metadata.namespace === write.namespace)) continue;
      items.push({ metadata: { name: write.name, ...(write.namespace ? { namespace: write.namespace } : {}) } } as T);
    }
  }
  return { ...data, items: items.sort(byName) };
}

export function applyEvent<T extends KubeObject>(items: readonly T[], event: { event: "ADDED" | "MODIFIED" | "DELETED"; object: T }): T[] {
  const id = identity(event.object);
  const others = items.filter((item) => identity(item) !== id);
  return (event.event === "DELETED" ? others : [...others, event.object]).sort(byName);
}

function isEvent(frame: EventFrame | BookmarkFrame): frame is EventFrame {
  return frame.type === "event";
}

function useSubscription(
  enabled: boolean,
  subscription: Subscription,
  key: readonly unknown[],
  version: () => string | undefined,
  onEvent: (frame: EventFrame) => void,
): LiveState {
  const multiplexer = useMultiplexer();
  const queryClient = useQueryClient();
  const cache = queryClient.getQueryCache();
  const filter = { queryKey: key, exact: true };
  const source = cache.find(filter);
  const [state, setState] = useState<LiveState>("connecting");
  const dependencyKey = JSON.stringify([key, subscription]);

  useEffect(() => {
    if (!enabled || !source) return;
    let watches = queryWatches.get(source);
    if (!watches) {
      watches = new Map();
      queryWatches.set(source, watches);
    }
    let watch = watches.get(multiplexer);
    if (!watch) {
      const shared: QueryWatch = { state: "connecting", listeners: new Set(), dispose: () => {} };
      const publish = (next: LiveState) => {
        shared.state = next;
        for (const listener of shared.listeners) listener(next);
      };
      let stop: (() => void) | undefined;
      const detach = () => {
        stop?.();
        stop = undefined;
      };
      const attach = () => {
        if (cache.get(source.queryHash) !== source) return;
        const snapshot = version();
        if (snapshot === undefined) return;
        let active = true;
        const current = () => active && cache.get(source.queryHash) === source;
        publish("connecting");
        const release = multiplexer.acquire(subscription, {
          resourceVersion: () => version() ?? snapshot,
          onEvent: (frame) => {
            if (current() && isEvent(frame)) onEvent(frame);
          },
          onRelist: () => {
            if (!current()) return;
            detach();
            void queryClient.invalidateQueries(filter, { cancelRefetch: false });
          },
          onError: () => { if (current()) publish("paused"); },
          onSubscribed: () => { if (current()) publish("live"); },
        });
        stop = () => {
          active = false;
          release();
        };
      };
      const unsubscribe = cache.subscribe((event) => {
        if (event.query !== source) return;
        if (event.type === "removed") detach();
        if (event.type === "updated" && event.action.type === "success" && !event.action.manual) {
          // Restart from the HTTP snapshot; the prior watch may have advanced past it.
          detach();
          attach();
        }
      });
      shared.dispose = () => {
        unsubscribe();
        detach();
      };
      watches.set(multiplexer, shared);
      watch = shared;
      attach();
    }
    watch.listeners.add(setState);
    setState(watch.state);
    return () => {
      watch.listeners.delete(setState);
      if (watch.listeners.size === 0) {
        watch.dispose();
        watches.delete(multiplexer);
      }
    };
  }, [cache, multiplexer, enabled, source, dependencyKey]);

  return enabled ? state : "paused";
}

export function useLiveList<T extends KubeObject = KubeObject>(
  ref: ResourceRef,
  scope: { namespace?: string; labelSelector?: string; listPath?: string; optimisticCreates?: boolean },
): UseQueryResult<KubeList<T>> & { live: LiveState } {
  const http = useHttp();
  const writes = usePendingWrites(http);
  const queryClient = useQueryClient();
  const client = kubeClient(http);
  const resourceKey = listKey(ref, scope.namespace, scope);
  const key = scope.listPath ? [...resourceKey, scope.listPath] : resourceKey;
  const listPath = scope.listPath;
  const query = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => listPath ? http.request<KubeList<T>>("GET", listPath, { signal }) : client.list<T>(ref, scope, signal),
    staleTime: listStaleMs,
  });
  const streamed = !listPath && ref.group === bedrockGroup;
  const subscription: Subscription = {
    group: ref.group,
    version: ref.version,
    resource: ref.plural,
    ...(scope.namespace === undefined ? {} : { namespace: scope.namespace }),
    ...(scope.labelSelector === undefined ? {} : { labelSelector: scope.labelSelector }),
  };

  const live = useSubscription(
    streamed,
    subscription,
    key,
    () => {
      const data = queryClient.getQueryData<KubeList<T>>(key);
      return data ? data.metadata.resourceVersion ?? "" : undefined;
    },
    (frame) =>
      queryClient.setQueryData<KubeList<T>>(key, (current) =>
        current
          ? { ...current, metadata: { ...current.metadata, resourceVersion: frame.resourceVersion }, items: applyEvent(current.items, { event: frame.event, object: frame.object as T }) }
          : current,
      ),
  );

  return Object.assign(query, { data: projectList(query.data, writes, ref, scope), live: query.isError || !query.data ? ("paused" as const) : live });
}

export function useLiveObject<T extends KubeObject = KubeObject>(
  ref: ResourceRef,
  namespace: string | undefined,
  name: string,
): UseQueryResult<T | null> & { live: LiveState } {
  const http = useHttp();
  const writes = usePendingWrites(http);
  const queryClient = useQueryClient();
  const client = kubeClient(http);
  const key = objectKey(ref, namespace, name);
  const source = () => queryClient.getQueryCache().find({ queryKey: key, exact: true });
  const fieldSelector = `metadata.name=${name}`;
  const subscriptionNamespace = namespace === undefined ? {} : { namespace };
  const streamed = ref.group === bedrockGroup;
  const query = useQuery({
    queryKey: key,
    queryFn: async ({ signal }) => {
      const entry = source();
      const refresh = entry?.state.data !== undefined;
      try {
        const object = await http.request<T>("GET", resourcePath(ref, { ...subscriptionNamespace, name }), { signal });
        if (entry && !signal.aborted) objectWatchVersions.delete(entry);
        return object;
      } catch (error) {
        if (!isHttpError(error) || error.status !== 404) throw error;
        if (!streamed || !refresh || !entry) return null;
        // A GET 404 has no cursor; a name-filtered list lets the watch catch a recreation.
        const snapshot = await client.list<T>(ref, { ...subscriptionNamespace, fieldSelector }, signal);
        signal.throwIfAborted();
        objectWatchVersions.set(entry, snapshot.metadata.resourceVersion ?? "");
        return snapshot.items[0] ?? null;
      }
    },
    staleTime: listStaleMs,
  });
  const subscription: Subscription = {
    group: ref.group,
    version: ref.version,
    resource: ref.plural,
    fieldSelector,
    ...subscriptionNamespace,
  };

  const live = useSubscription(
    streamed,
    subscription,
    key,
    () => {
      const data = queryClient.getQueryData<T | null>(key);
      const entry = source();
      if (data === undefined || !entry) return undefined;
      return objectWatchVersions.get(entry) ?? (data ? data.metadata.resourceVersion ?? "" : undefined);
    },
    (frame) => {
      const entry = source();
      if (entry) objectWatchVersions.set(entry, frame.resourceVersion);
      queryClient.setQueryData<T | null>(key, frame.event === "DELETED" ? null : (frame.object as T));
    },
  );

  return Object.assign(query, {
    data: query.data ? applyDesired(query.data, writes, ref) : query.data,
    live: query.isError || !query.data ? ("paused" as const) : live,
  });
}
