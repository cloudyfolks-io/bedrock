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

export function networkCidrs(object: KubeObject): string {
  const cidrs = listAt(object, "spec.cidrs");
  return cidrs.length > 0 ? cidrs.join(", ") : textAt(object, "spec.cidr");
}

export function osSummary(object: KubeObject): string {
  return [textAt(object, "spec.os.family"), textAt(object, "spec.os.version")].filter((part) => part !== "").join(" ");
}

export function isIp(text: string): boolean {
  const ipv4 = /^(25[0-5]|2[0-4]\d|1?\d?\d)(\.(25[0-5]|2[0-4]\d|1?\d?\d)){3}$/;
  if (ipv4.test(text)) {
    return true;
  }
  if (!text.includes(":") || !/^[0-9A-Fa-f:.]+$/.test(text) || text.split("::").length > 2) {
    return false;
  }
  try {
    return new URL(`http://[${text}]/`).hostname !== "";
  } catch {
    return false;
  }
}

export function cidrSummary(cidr: string): string {
  const [address, prefixText] = cidr.split("/");
  const prefix = Number(prefixText);
  if (!address || prefixText === undefined || !Number.isInteger(prefix) || !isIp(address)) {
    return cidr;
  }
  const bits = address.includes(":") ? 128 : 32;
  const host = bits - prefix;
  if (host < 0) {
    return cidr;
  }
  return host > 30 ? `${cidr} (2^${host} addresses)` : `${cidr} (${2 ** host} addresses)`;
}

interface Rule {
  protocol?: string;
  port?: number;
  endPort?: number;
  cidr?: string;
  securityGroupRef?: string;
}

export function ruleSummary(rule: Rule): string {
  const ports = rule.port === undefined ? "" : rule.endPort === undefined ? ` ${rule.port}` : ` ${rule.port}-${rule.endPort}`;
  const source = rule.cidr ? ` from ${rule.cidr}` : rule.securityGroupRef ? ` from group ${rule.securityGroupRef}` : "";
  return `${rule.protocol ?? "all"}${ports}${source}`;
}

export function exposesSsh(rule: Rule): boolean {
  const open = rule.cidr === "0.0.0.0/0" || rule.cidr === "::/0";
  const protocolHasPorts = rule.protocol === "tcp" || rule.protocol === "all" || rule.protocol === undefined;
  const covers = rule.port === undefined ? true : rule.port <= 22 && (rule.endPort ?? rule.port) >= 22;
  return open && protocolHasPorts && covers;
}
