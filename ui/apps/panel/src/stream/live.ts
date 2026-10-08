import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import { useHttp } from "../api/HttpProvider";
import { isHttpError } from "../api/http";
import { kubeClient, listKey, objectKey, type ResourceRef } from "../api/kube";
import type { KubeList, KubeObject } from "../api/types";
import { useMultiplexer } from "./StreamProvider";
import { bedrockGroup, type BookmarkFrame, type EventFrame, type Subscription } from "./protocol";

export type LiveState = "connecting" | "live" | "paused";

const listStaleMs = 30_000;

function identity(object: KubeObject): string {
  return object.metadata.uid ?? `${object.metadata.namespace ?? ""}/${object.metadata.name}`;
}

function byName(left: KubeObject, right: KubeObject): number {
  return left.metadata.name.localeCompare(right.metadata.name) || (left.metadata.namespace ?? "").localeCompare(right.metadata.namespace ?? "");
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
  version: () => string,
  onEvent: (frame: EventFrame) => void,
  onRelist: () => void,
  dependencyKey: string,
): LiveState {
  const multiplexer = useMultiplexer();
  const [state, setState] = useState<LiveState>("connecting");
  const latest = useRef({ version, onEvent, onRelist });
  latest.current = { version, onEvent, onRelist };

  useEffect(() => {
    if (!enabled) {
      return undefined;
    }
    setState("connecting");
    return multiplexer.acquire(subscription, {
      resourceVersion: () => latest.current.version(),
      onEvent: (frame) => {
        if (isEvent(frame)) {
          latest.current.onEvent(frame);
        }
      },
      onRelist: () => latest.current.onRelist(),
      onError: () => setState("paused"),
      onSubscribed: () => setState("live"),
    });
  }, [multiplexer, enabled, dependencyKey]);

  return enabled ? state : "paused";
}

export function useLiveList<T extends KubeObject = KubeObject>(
  ref: ResourceRef,
  scope: { namespace?: string; labelSelector?: string },
): UseQueryResult<KubeList<T>> & { live: LiveState } {
  const http = useHttp();
  const queryClient = useQueryClient();
  const client = kubeClient(http);
  const key = listKey(ref, scope.namespace, scope);
  const query = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => client.list<T>(ref, scope, signal),
    staleTime: listStaleMs,
  });
  const [generation, setGeneration] = useState(0);
  const loaded = query.data !== undefined;
  const streamed = loaded && ref.group === bedrockGroup;
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
    () => queryClient.getQueryData<KubeList<T>>(key)?.metadata.resourceVersion ?? "",
    (frame) =>
      queryClient.setQueryData<KubeList<T>>(key, (current) =>
        current
          ? { ...current, metadata: { ...current.metadata, resourceVersion: frame.resourceVersion }, items: applyEvent(current.items, { event: frame.event, object: frame.object as T }) }
          : current,
      ),
    () => {
      void queryClient.invalidateQueries({ queryKey: key }).then(() => setGeneration((value) => value + 1));
    },
    `${JSON.stringify(key)}#${generation}`,
  );

  return Object.assign(query, { live: query.isError ? ("paused" as const) : live });
}

export function useLiveObject<T extends KubeObject = KubeObject>(
  ref: ResourceRef,
  namespace: string | undefined,
  name: string,
): UseQueryResult<T | null> & { live: LiveState } {
  const http = useHttp();
  const queryClient = useQueryClient();
  const client = kubeClient(http);
  const key = objectKey(ref, namespace, name);
  const query = useQuery({
    queryKey: key,
    queryFn: () =>
      client.get<T>(ref, namespace, name).catch((error: unknown) => {
        if (isHttpError(error) && error.status === 404) {
          return null;
        }
        throw error;
      }),
    staleTime: listStaleMs,
  });
  const [generation, setGeneration] = useState(0);
  const present = query.data !== undefined && query.data !== null;
  const subscription: Subscription = {
    group: ref.group,
    version: ref.version,
    resource: ref.plural,
    fieldSelector: `metadata.name=${name}`,
    ...(namespace === undefined ? {} : { namespace }),
  };

  const live = useSubscription(
    present && ref.group === bedrockGroup,
    subscription,
    () => queryClient.getQueryData<T | null>(key)?.metadata.resourceVersion ?? "",
    (frame) => queryClient.setQueryData<T | null>(key, frame.event === "DELETED" ? null : (frame.object as T)),
    () => {
      void queryClient.invalidateQueries({ queryKey: key }).then(() => setGeneration((value) => value + 1));
    },
    `${JSON.stringify(key)}#${generation}`,
  );

  return Object.assign(query, { live: query.isError ? ("paused" as const) : live });
}
