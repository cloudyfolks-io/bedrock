import type { ReactNode } from "react";
import { Link } from "react-router";
import { msg } from "@lingui/core/macro";
import type { MessageDescriptor } from "@lingui/core";
import { Badge } from "@bedrock/design";
import type { KubeObject } from "../api/types";
import { Ltr } from "../components/Ltr";
import { PhaseBadge, StatusBadge } from "../components/StatusBadge";
import { formatBytes, formatQuantity, formatRelative } from "../format";
import type { Locale } from "../i18n";
import { listAt, textAt, valueAt } from "./path";
import { listenersOf, memberCount, networkCidrs, osSummary, ownerCount } from "./summaries";

export interface ColumnContext {
  locale: Locale;
  now: Date;
  detailPath: (object: KubeObject) => string | undefined;
  project?: string;
  elasticIps?: KubeObject[];
}

export interface ColumnDef {
  id: string;
  label: MessageDescriptor;
  render: (object: KubeObject, context: ColumnContext) => ReactNode;
  sortValue?: (object: KubeObject) => string | number;
}

const dash = "—";

function text(id: string, label: MessageDescriptor, path: string): ColumnDef {
  return { id, label, render: (object) => textAt(object, path) || dash, sortValue: (object) => textAt(object, path) };
}

function ltrText(id: string, label: MessageDescriptor, path: string): ColumnDef {
  return { id, label, render: (object) => (textAt(object, path) ? <Ltr>{textAt(object, path)}</Ltr> : dash), sortValue: (object) => textAt(object, path) };
}

function count(id: string, label: MessageDescriptor, compute: (object: KubeObject) => number): ColumnDef {
  return { id, label, render: (object) => String(compute(object)), sortValue: compute };
}

function flag(id: string, label: MessageDescriptor, path: string): ColumnDef {
  return {
    id,
    label,
    render: (object) => (valueAt(object, path) === true ? <Badge tone="info">{"✓"}</Badge> : dash),
    sortValue: (object) => (valueAt(object, path) === true ? 1 : 0),
  };
}

const name: ColumnDef = {
  id: "name",
  label: msg({ id: "col.name", message: "Name" }),
  render: (object, context) => {
    const to = context.detailPath(object);
    return to ? <Link to={to}>{object.metadata.name}</Link> : object.metadata.name;
  },
  sortValue: (object) => object.metadata.name,
};

const status: ColumnDef = {
  id: "status",
  label: msg({ id: "col.status", message: "Status" }),
  render: (object) => <StatusBadge obj={object} />,
  sortValue: (object) => textAt(object, "status.phase"),
};

const phase: ColumnDef = {
  id: "phase",
  label: msg({ id: "col.phase", message: "Phase" }),
  render: (object) => (textAt(object, "status.phase") ? <PhaseBadge phase={textAt(object, "status.phase")} /> : dash),
  sortValue: (object) => textAt(object, "status.phase"),
};

const age: ColumnDef = {
  id: "age",
  label: msg({ id: "col.age", message: "Age" }),
  render: (object, context) => (object.metadata.creationTimestamp ? formatRelative(object.metadata.creationTimestamp, context.now, context.locale) : dash),
  sortValue: (object) => object.metadata.creationTimestamp ?? "",
};

const sizeBytes = (id: string, label: MessageDescriptor, path: string): ColumnDef => ({
  id,
  label,
  render: (object, context) => {
    const value = valueAt(object, path);
    return typeof value === "number" ? formatBytes(value, context.locale) : dash;
  },
  sortValue: (object) => Number(valueAt(object, path) ?? 0),
});

const gi = (id: string, label: MessageDescriptor, path: string): ColumnDef => ({
  id,
  label,
  render: (object) => (valueAt(object, path) === undefined ? dash : `${String(valueAt(object, path))} GiB`),
  sortValue: (object) => Number(valueAt(object, path) ?? 0),
});

const typeBadge: ColumnDef = {
  id: "type",
  label: msg({ id: "col.type", message: "Type" }),
  render: (object) => (textAt(object, "spec.type") ? <Badge tone="info">{textAt(object, "spec.type")}</Badge> : dash),
  sortValue: (object) => textAt(object, "spec.type"),
};

const roles: ColumnDef = {
  id: "roles",
  label: msg({ id: "col.roles", message: "Roles" }),
  render: (object) => (
    <span className="br-chips">
      {listAt(object, "spec.roles").map((role) => (
        <Badge key={String(role)} tone="neutral">
          {String(role)}
        </Badge>
      ))}
    </span>
  ),
  sortValue: (object) => listAt(object, "spec.roles").join(","),
};

const containerAddresses: ColumnDef = {
  id: "addresses",
  label: msg({ id: "col.addresses", message: "Addresses" }),
  render: (object) => {
    const ips = listAt(object, "status.addresses").map((entry) => String(entry)).filter((ip) => ip !== "");
    return ips.length === 0 ? dash : ips.map((ip) => <Ltr key={ip}>{`${ip} `}</Ltr>);
  },
  sortValue: (object) => String(listAt(object, "status.addresses")[0] ?? ""),
};

const replicas: ColumnDef = {
  id: "replicas",
  label: msg({ id: "col.replicas", message: "Replicas" }),
  render: (object) => (valueAt(object, "status.replicas") === undefined ? dash : `${String(valueAt(object, "status.readyReplicas") ?? 0)}/${String(valueAt(object, "status.replicas"))}`),
  sortValue: (object) => Number(valueAt(object, "status.readyReplicas") ?? 0),
};

function holderPath(project: string, holder: string): string {
  return holder.startsWith("Container/") ? `/p/${project}/containers/${holder.slice("Container/".length)}` : `/p/${project}/vms/${holder}`;
}

const addresses: ColumnDef = {
  id: "addresses",
  label: msg({ id: "col.addresses", message: "Addresses" }),
  render: (object) => {
    const ips = (listAt(object, "status.addresses") as { ip?: string }[]).map((entry) => entry.ip ?? "").filter((ip) => ip !== "");
    return ips.length === 0 ? dash : ips.map((ip) => <Ltr key={ip}>{`${ip} `}</Ltr>);
  },
  sortValue: (object) => String((listAt(object, "status.addresses")[0] as { ip?: string } | undefined)?.ip ?? ""),
};

const target: ColumnDef = {
  id: "target",
  label: msg({ id: "col.target", message: "Target" }),
  render: (object) => (textAt(object, "spec.target.name") ? <Ltr>{`${textAt(object, "spec.target.kind")} ${textAt(object, "spec.target.name")}`}</Ltr> : dash),
  sortValue: (object) => textAt(object, "spec.target.name"),
};

const quota = (id: string, label: MessageDescriptor, path: string): ColumnDef => ({
  id,
  label,
  render: (object) => {
    const value = textAt(object, path);
    return value ? formatQuantity(value) : dash;
  },
  sortValue: (object) => textAt(object, path),
});

const version = (id: string, label: MessageDescriptor, path: string): ColumnDef => ltrText(id, label, path);

const L = {
  displayName: msg({ id: "col.displayName", message: "Display name" }),
  node: msg({ id: "col.node", message: "Node" }),
  instanceType: msg({ id: "col.instanceType", message: "Instance type" }),
  maintenance: msg({ id: "col.maintenance", message: "Maintenance" }),
  version: msg({ id: "col.version", message: "Version" }),
  desiredVersion: msg({ id: "col.desiredVersion", message: "Desired version" }),
  k0sVersion: msg({ id: "col.k0sVersion", message: "k0s version" }),
  image: msg({ id: "col.image", message: "Image" }),
  value: msg({ id: "col.value", message: "Value" }),
  default: msg({ id: "col.default", message: "Default" }),
  enabled: msg({ id: "col.enabled", message: "Enabled" }),
  owners: msg({ id: "col.owners", message: "Owners" }),
  projects: msg({ id: "col.projects", message: "Project quota" }),
  vpcCidr: msg({ id: "col.vpcCidr", message: "VPC CIDR" }),
  tenant: msg({ id: "col.tenant", message: "Tenant" }),
  namespace: msg({ id: "col.namespace", message: "Namespace" }),
  members: msg({ id: "col.members", message: "Members" }),
  type: msg({ id: "col.type", message: "Type" }),
  os: msg({ id: "col.os", message: "OS" }),
  size: msg({ id: "col.size", message: "Size" }),
  versions: msg({ id: "col.versions", message: "Versions" }),
  cidr: msg({ id: "col.cidr", message: "CIDR" }),
  external: msg({ id: "col.external", message: "External" }),
  vpc: msg({ id: "col.vpc", message: "VPC" }),
  storageClass: msg({ id: "col.storageClass", message: "Storage class" }),
  attachedTo: msg({ id: "col.attachedTo", message: "Attached to" }),
  source: msg({ id: "col.source", message: "Source" }),
  volume: msg({ id: "col.volume", message: "Volume" }),
  retention: msg({ id: "col.retention", message: "Retention" }),
  digest: msg({ id: "col.digest", message: "Digest" }),
  vm: msg({ id: "col.vm", message: "Virtual machine" }),
  backupTarget: msg({ id: "col.backupTarget", message: "Target" }),
  schedule: msg({ id: "col.schedule", message: "Schedule" }),
  keep: msg({ id: "col.keep", message: "Keep" }),
  backup: msg({ id: "col.backup", message: "Backup" }),
  targetName: msg({ id: "col.targetName", message: "Restored name" }),
  replace: msg({ id: "col.replace", message: "Replace" }),
  network: msg({ id: "col.network", message: "Network" }),
  gateway: msg({ id: "col.gateway", message: "Gateway" }),
  dhcp: msg({ id: "col.dhcp", message: "DHCP" }),
  dnsZone: msg({ id: "col.dnsZone", message: "DNS zone" }),
  providerNetwork: msg({ id: "col.providerNetwork", message: "Provider network" }),
  address: msg({ id: "col.address", message: "Address" }),
  elasticIp: msg({ id: "col.elasticIp", message: "Elastic IP" }),
  listeners: msg({ id: "col.listeners", message: "Listeners" }),
  zone: msg({ id: "col.zone", message: "Zone" }),
  records: msg({ id: "col.records", message: "Records" }),
  ingress: msg({ id: "col.ingress", message: "Ingress rules" }),
  egress: msg({ id: "col.egress", message: "Egress rules" }),
  user: msg({ id: "col.user", message: "User" }),
  endpoint: msg({ id: "col.endpoint", message: "Endpoint" }),
  versioning: msg({ id: "col.versioning", message: "Versioning" }),
  quota: msg({ id: "col.quota", message: "Quota" }),
  buckets: msg({ id: "col.buckets", message: "Bucket quota" }),
  keyType: msg({ id: "col.keyType", message: "Key type" }),
};

const poolOf = (object: KubeObject): string => textAt(object, "status.providerNetwork") || textAt(object, "spec.providerNetwork");

const sourceSummary = (object: KubeObject): string =>
  textAt(object, "spec.source.snapshotRef")
    ? `snapshot ${textAt(object, "spec.source.snapshotRef")}`
    : textAt(object, "spec.source.volumeRef")
      ? `volume ${textAt(object, "spec.source.volumeRef")}`
      : textAt(object, "spec.source.image.name")
        ? `image ${textAt(object, "spec.source.image.name")}`
        : "";

const lbAddress: ColumnDef = {
  id: "elasticIp",
  label: msg({ id: "col.elasticIp", message: "Elastic IP" }),
  render: (object, context) => {
    const eip = context.elasticIps?.find((candidate) => textAt(candidate, "spec.target.kind") === "LoadBalancer" && textAt(candidate, "spec.target.name") === object.metadata.name);
    return eip ? <Ltr>{[eip.metadata.name, textAt(eip, "status.address")].filter((part) => part !== "").join(" ")}</Ltr> : dash;
  },
  sortValue: (object) => object.metadata.name,
};

export const columnsByPlural: Record<string, ColumnDef[]> = {
  hosts: [
    name,
    roles,
    { ...status, id: "ready" },
    flag("maintenance", L.maintenance, "spec.maintenance"),
    version("kubernetesVersion", L.version, "status.kubernetesVersion"),
    age,
  ],
  clusters: [name, version("version", L.version, "status.version"), version("desiredVersion", L.desiredVersion, "spec.desiredVersion"), phase, age],
  releases: [version("version", L.version, "spec.version"), version("k0sVersion", L.k0sVersion, "spec.k0sVersion"), ltrText("image", L.image, "spec.image"), age],
  settings: [name, text("value", L.value, "spec.value"), text("default", L.default, "status.default")],
  addons: [name, flag("enabled", L.enabled, "spec.enabled"), status, age],
  tenants: [name, text("displayName", L.displayName, "spec.displayName"), count("owners", L.owners, ownerCount), text("projects", L.projects, "spec.quota.projects"), ltrText("vpcCidr", L.vpcCidr, "spec.network.vpcCIDR"), age],
  projects: [name, text("displayName", L.displayName, "spec.displayName"), text("tenant", L.tenant, "spec.tenantRef"), ltrText("namespace", L.namespace, "status.namespace"), count("members", L.members, memberCount), age],
  clusterimages: [
    name,
    typeBadge,
    { id: "os", label: L.os, render: (object) => osSummary(object) || dash, sortValue: osSummary },
    phase,
    sizeBytes("size", L.size, "status.sizeBytes"),
    count("versions", L.versions, (object) => listAt(object, "status.versions").length),
  ],
  networks: [name, text("tenant", L.tenant, "spec.tenantRef"), { id: "cidr", label: L.cidr, render: (object) => (networkCidrs(object) ? <Ltr>{networkCidrs(object)}</Ltr> : dash), sortValue: networkCidrs }, flag("external", L.external, "spec.enableExternal"), text("vpc", L.vpc, "status.vpc"), status],
  virtualmachines: [name, phase, text("node", L.node, "status.node"), addresses, text("instanceType", L.instanceType, "spec.instanceType"), age],
  containers: [name, phase, ltrText("image", L.image, "spec.image"), replicas, containerAddresses, age],
  sshkeys: [
    name,
    { id: "keyType", label: L.keyType, render: (object) => textAt(object, "spec.publicKey").split(" ")[0] || dash, sortValue: (object) => textAt(object, "spec.publicKey").split(" ")[0] ?? "" },
    age,
  ],
  volumes: [name, phase, gi("size", L.size, "spec.sizeGi"), text("storageClass", L.storageClass, "spec.storageClass"), { id: "attachedTo", label: L.attachedTo, render: (object, context) => (textAt(object, "status.attachedTo") ? (context.project ? <Link to={holderPath(context.project, textAt(object, "status.attachedTo"))}>{textAt(object, "status.attachedTo")}</Link> : textAt(object, "status.attachedTo")) : dash), sortValue: (object) => textAt(object, "status.attachedTo") }, { id: "source", label: L.source, render: (object) => sourceSummary(object) || dash, sortValue: sourceSummary }],
  volumesnapshots: [name, text("volume", L.volume, "spec.volumeRef"), phase, text("retention", L.retention, "spec.retention"), age],
  images: [name, typeBadge, phase, { id: "os", label: L.os, render: (object) => osSummary(object) || dash, sortValue: osSummary }, sizeBytes("size", L.size, "status.sizeBytes"), ltrText("digest", L.digest, "status.digest")],
  virtualmachinebackups: [name, text("vm", L.vm, "spec.vmRef"), text("target", L.backupTarget, "spec.target"), ltrText("schedule", L.schedule, "spec.schedule"), text("keep", L.keep, "spec.keep"), phase],
  virtualmachinerestores: [name, text("backup", L.backup, "spec.backupRef"), text("targetName", L.targetName, "spec.targetName"), flag("replace", L.replace, "spec.replace"), phase, age],
  subnets: [name, text("network", L.network, "spec.networkRef"), ltrText("cidr", L.cidr, "spec.cidr"), ltrText("gateway", L.gateway, "spec.gateway"), flag("dhcp", L.dhcp, "spec.dhcp"), text("dnsZone", L.dnsZone, "spec.dnsZoneRef")],
  elasticips: [name, { id: "providerNetwork", label: L.providerNetwork, render: (object) => poolOf(object) || dash, sortValue: poolOf }, { ...ltrText("address", L.address, "status.address") }, target, status],
  loadbalancers: [name, lbAddress, { id: "listeners", label: L.listeners, render: (object) => listenersOf(object) || dash, sortValue: listenersOf }, status],
  dnszones: [name, ltrText("zone", L.zone, "spec.zone"), { id: "records", label: L.records, render: (object) => (<span className="br-records">{(listAt(object, "spec.records") as { name: string; ips: string[] }[]).map((record) => (<Ltr key={record.name}>{`${record.name}: ${record.ips.join(", ")} `}</Ltr>))}</span>), sortValue: (object) => listAt(object, "spec.records").length }, status],
  securitygroups: [name, count("ingress", L.ingress, (object) => listAt(object, "spec.ingress").length), count("egress", L.egress, (object) => listAt(object, "spec.egress").length), status],
  objectstoragebuckets: [
    name,
    text("user", L.user, "spec.userRef"),
    ltrText("endpoint", L.endpoint, "status.endpoint"),
    flag("versioning", L.versioning, "spec.versioning"),
    quota("quota", L.quota, "spec.quota.bytes"),
    status,
  ],
  objectstorageusers: [name, text("displayName", L.displayName, "spec.displayName"), text("buckets", L.buckets, "spec.quota.buckets"), quota("quota", L.quota, "spec.quota.bytes"), status],
};
