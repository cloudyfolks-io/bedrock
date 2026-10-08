import type { KubeObject } from "../api/types";
import { listAt, textAt } from "./path";

interface Listener {
  protocol?: string;
  port?: number;
}

export function listenerSummary(listener: Listener): string {
  return `${listener.protocol ?? ""} ${listener.port ?? ""}`.trim();
}

export function listenersOf(object: KubeObject): string {
  return (listAt(object, "spec.listeners") as Listener[]).map(listenerSummary).join(", ");
}

export function ownerCount(object: KubeObject): number {
  return listAt(object, "spec.owners").length;
}

export function memberCount(object: KubeObject): number {
  return listAt(object, "spec.members").length;
}

export function osSummary(object: KubeObject): string {
  return [textAt(object, "spec.os.family"), textAt(object, "spec.os.version")].filter((part) => part !== "").join(" ");
}
