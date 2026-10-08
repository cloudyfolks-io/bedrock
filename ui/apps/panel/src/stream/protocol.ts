import type { KubeObject } from "../api/types";

export const streamSubprotocol = "bedrock.stream.v1";
export const bedrockGroup = "bedrock.cloudyfolks.io";

export interface Subscription {
  group: string;
  version: string;
  resource: string;
  namespace?: string;
  labelSelector?: string;
  fieldSelector?: string;
}

export type ClientFrame =
  | ({ op: "subscribe"; id: string; resourceVersion: string } & Required<Omit<Subscription, "namespace" | "labelSelector" | "fieldSelector">> & {
        namespace: string;
        labelSelector: string;
        fieldSelector: string;
      })
  | { op: "unsubscribe"; id: string }
  | { op: "ping" };

export interface EventFrame {
  type: "event";
  id: string;
  event: "ADDED" | "MODIFIED" | "DELETED";
  resourceVersion: string;
  object: KubeObject;
}

export interface BookmarkFrame {
  type: "bookmark";
  id: string;
  resourceVersion: string;
}

export type ServerFrame =
  | { type: "hello"; expiresAt: string; limits: { maxSubscriptions: number } }
  | { type: "subscribed"; id: string }
  | EventFrame
  | BookmarkFrame
  | { type: "relist"; id: string }
  | { type: "error"; id?: string; code: string; message: string }
  | { type: "pong" };

export type StreamKey = string;

export function subscriptionKey(sub: Subscription): StreamKey {
  return JSON.stringify([sub.group, sub.version, sub.resource, sub.namespace ?? "", sub.labelSelector ?? "", sub.fieldSelector ?? ""]);
}

export interface WebSocketLike {
  readonly readyState: number;
  onopen: (() => void) | null;
  onmessage: ((event: { data: string }) => void) | null;
  onclose: ((event: { code: number }) => void) | null;
  onerror: (() => void) | null;
  send(data: string): void;
  close(code?: number): void;
}

export interface StreamHandlers {
  resourceVersion: () => string;
  onEvent: (frame: EventFrame | BookmarkFrame) => void;
  onRelist: () => void;
  onError: (code: string, message: string) => void;
  onSubscribed?: () => void;
}

export type StreamStatus = "idle" | "connecting" | "live" | "reconnecting" | "ended";

export function parseServerFrame(text: string): ServerFrame | undefined {
  try {
    const value = JSON.parse(text) as { type?: unknown };
    return value && typeof value === "object" && typeof value.type === "string" ? (value as ServerFrame) : undefined;
  } catch {
    return undefined;
  }
}
