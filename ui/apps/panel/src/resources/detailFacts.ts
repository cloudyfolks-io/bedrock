import type { MessageDescriptor } from "@lingui/core";
import { msg } from "@lingui/core/macro";
import type { KubeObject } from "../api/types";
import { formatBytes, formatDateTime, formatNumber, formatQuantity } from "../format";
import type { Locale } from "../i18n";
import { listAt, valueAt } from "./path";
import { osSummary } from "./summaries";
import { vmSize } from "./vmSize";

const L = {
  addresses: msg({ id: "col.addresses", message: "Addresses" }),
  address: msg({ id: "col.address", message: "Address" }),
  addressPort: msg({ id: "detail.field.addressPort", message: "Address and port" }),
  applied: msg({ id: "detail.field.appliedValue", message: "Applied value" }),
  internalAddress: msg({ id: "detail.field.internalAddress", message: "Internal address" }),
  proxyAddress: msg({ id: "detail.field.proxyAddress", message: "Proxy address" }),
  attachedTo: msg({ id: "col.attachedTo", message: "Attached to" }),
  backup: msg({ id: "col.backup", message: "Backup" }),
  backupId: msg({ id: "detail.field.backupId", message: "Backup ID" }),
  backupsEnabled: msg({ id: "detail.field.backupsEnabled", message: "Backups enabled" }),
  bucketName: msg({ id: "col.bucketName", message: "Bucket name" }),
  buckets: msg({ id: "col.buckets", message: "Buckets" }),
  cidr: msg({ id: "col.cidr", message: "CIDR" }),
  class: msg({ id: "col.class", message: "Class" }),
  connectCidr: msg({ id: "col.connectCidr", message: "Connect network" }),
  cpu: msg({ id: "col.cpu", message: "CPU" }),
  database: msg({ id: "col.database", message: "Database" }),
  databaseName: msg({ id: "col.databaseName", message: "Database name" }),
  default: msg({ id: "col.default", message: "Default" }),
  desiredVersion: msg({ id: "col.desiredVersion", message: "Desired version" }),
  dhcp: msg({ id: "col.dhcp", message: "DHCP" }),
  digest: msg({ id: "col.digest", message: "Digest" }),
  displayName: msg({ id: "col.displayName", message: "Display name" }),
  dnsZone: msg({ id: "col.dnsZone", message: "DNS zone" }),
  egress: msg({ id: "col.egress", message: "Egress rules" }),
  elasticIp: msg({ id: "col.elasticIp", message: "Elastic IP" }),
  enabled: msg({ id: "col.enabled", message: "Enabled" }),
  endpoint: msg({ id: "col.endpoint", message: "Endpoint" }),
  engine: msg({ id: "col.engine", message: "Engine" }),
  eolDate: msg({ id: "col.eolDate", message: "End of life" }),
  external: msg({ id: "col.external", message: "External" }),
  gateway: msg({ id: "col.gateway", message: "Gateway" }),
  grants: msg({ id: "col.grants", message: "Grants" }),
  highAvailability: msg({ id: "col.highAvailability", message: "High availability" }),
  host: msg({ id: "col.host", message: "Host" }),
  image: msg({ id: "col.image", message: "Image" }),
  ingress: msg({ id: "col.ingress", message: "Ingress rules" }),
  instances: msg({ id: "detail.field.instances", message: "Instances" }),
  instanceType: msg({ id: "col.instanceType", message: "Instance type" }),
  k0sVersion: msg({ id: "col.k0sVersion", message: "k0s version" }),
  keep: msg({ id: "col.keep", message: "Keep" }),
  keyType: msg({ id: "col.keyType", message: "Key type" }),
  lastBackup: msg({ id: "detail.field.lastBackup", message: "Last backup" }),
  listeners: msg({ id: "col.listeners", message: "Listeners" }),
  maintenance: msg({ id: "col.maintenance", message: "Maintenance" }),
  maxStorage: msg({ id: "col.maxStorage", message: "Maximum storage" }),
  members: msg({ id: "col.members", message: "Members" }),
  memory: msg({ id: "col.memory", message: "Memory" }),
  minStorage: msg({ id: "col.minStorage", message: "Minimum storage" }),
  namespace: msg({ id: "col.namespace", message: "Namespace" }),
  network: msg({ id: "col.network", message: "Network" }),
  networks: msg({ id: "col.networks", message: "Networks" }),
  node: msg({ id: "col.node", message: "Node" }),
  os: msg({ id: "col.os", message: "OS" }),
  owners: msg({ id: "col.owners", message: "Owners" }),
  permission: msg({ id: "col.permission", message: "Permission" }),
  policy: msg({ id: "col.policy", message: "On delete" }),
  project: msg({ id: "detail.field.projectName", message: "Project name" }),
  providerNetwork: msg({ id: "col.providerNetwork", message: "Provider network" }),
  quota: msg({ id: "col.quota", message: "Quota" }),
  records: msg({ id: "col.records", message: "Records" }),
  registry: msg({ id: "col.registry", message: "Registry" }),
  replace: msg({ id: "col.replace", message: "Replace" }),
  repositories: msg({ id: "col.repositories", message: "Repositories" }),
  replicas: msg({ id: "detail.field.readyReplicas", message: "Ready replicas" }),
  resultImage: msg({ id: "detail.field.resultImage", message: "Result image" }),
  retention: msg({ id: "col.retention", message: "Retention" }),
  revision: msg({ id: "detail.field.revision", message: "Revision" }),
  roles: msg({ id: "col.roles", message: "Roles" }),
  schedule: msg({ id: "col.schedule", message: "Schedule" }),
  size: msg({ id: "col.size", message: "Size" }),
  source: msg({ id: "col.source", message: "Source" }),
  storageClass: msg({ id: "col.storageClass", message: "Storage class" }),
  subnet: msg({ id: "col.subnet", message: "Subnet" }),
  suspended: msg({ id: "col.suspended", message: "Suspended" }),
  target: msg({ id: "col.target", message: "Target" }),
  targetName: msg({ id: "col.targetName", message: "Restored name" }),
  tenant: msg({ id: "col.tenant", message: "Tenant" }),
  type: msg({ id: "col.type", message: "Type" }),
  usage: msg({ id: "col.usage", message: "Used" }),
  username: msg({ id: "col.username", message: "User name" }),
  requested: msg({ id: "detail.field.requestedValue", message: "Requested value" }),
  version: msg({ id: "col.version", message: "Version" }),
  versioning: msg({ id: "col.versioning", message: "Versioning" }),
  versions: msg({ id: "col.versions", message: "Versions" }),
  vm: msg({ id: "col.vm", message: "Virtual machine" }),
  volume: msg({ id: "col.volume", message: "Volume" }),
  vpc: msg({ id: "col.vpc", message: "VPC" }),
  vpcCidr: msg({ id: "col.vpcCidr", message: "VPC CIDR" }),
  workers: msg({ id: "col.workers", message: "Workers" }),
  zone: msg({ id: "col.zone", message: "Zone" }),
  ports: msg({ id: "sg.ports", message: "Ports" }),
  port: msg({ id: "detail.field.port", message: "Port" }),
  publicKey: msg({ id: "detail.field.publicKey", message: "Public key" }),
  objects: msg({ id: "detail.field.objects", message: "Objects" }),
  finishedAt: msg({ id: "detail.field.finishedAt", message: "Finished at" }),
  unlimited: msg({ id: "registry.quota.unlimited", message: "Unlimited" }),
};

type Scalar = string | number | boolean;
interface FactOptions {
  copy?: boolean;
  link?: string;
}
interface DetailValue extends FactOptions {
  value: Scalar | MessageDescriptor;
}
export interface DetailFact {
  label: MessageDescriptor;
  values: DetailValue[];
}

function scalar(value: unknown): Scalar | undefined {
  if (typeof value === "string") return safeText(value) || undefined;
  if (typeof value === "boolean" || (typeof value === "number" && Number.isFinite(value))) return value;
  return undefined;
}

function text(object: unknown, path: string) {
  return scalar(valueAt(object, path))?.toString() ?? "";
}

function nestedList(object: unknown, path: string): unknown[] {
  const value = valueAt(object, path);
  return Array.isArray(value) ? value : [];
}

function safeText(value: string): string {
  if (/-----BEGIN [^\r\n]*PRIVATE KEY-----/.test(value)) return "";
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(value)) {
    try {
      const url = new URL(value);
      if (url.username || url.password || url.search || url.hash) {
        url.username = url.password = url.search = url.hash = "";
        return url.href;
      }
    } catch {
      return "";
    }
  }
  return value;
}

// Secret references contain object names, not Secret data. Do not show opaque
// inputs that can contain inline credentials, even in the advanced view.
export function specificationJSON(plural: string, spec: KubeObject["spec"]): string {
  return JSON.stringify(spec ?? {}, (key, value: unknown) => {
    if (
      (!/secret(?:key)?refs?$/i.test(key) && /secret|password|token|credential|privateKey|dockerconfigjson/i.test(key) && key !== "imagePullSecrets") ||
      (plural === "containers" && /^(env|command|args)$/.test(key)) ||
      (plural === "virtualmachines" && /^(userData|networkData)$/.test(key)) ||
      (plural === "addons" && key === "values")
    ) return "[redacted]";
    return typeof value === "string" ? safeText(value) || (value ? "[redacted]" : "") : value;
  }, 2);
}

function addressPort(address: string, port: string) {
  if (!address) return "";
  if (!port) return address;
  return `${address.includes(":") && !address.startsWith("[") ? `[${address}]` : address}:${port}`;
}

function listener(entry: unknown) {
  const front = [text(entry, "protocol"), text(entry, "port")].filter(Boolean).join(" ");
  const targets = nestedList(entry, "targets").map((target) =>
    addressPort(text(target, "vmRef") || text(target, "containerRef"), text(target, "port")),
  ).filter(Boolean);
  return [front, targets.join(", ")].filter(Boolean).join(" · ");
}

export function detailFacts(plural: string, obj: KubeObject, locale: Locale): DetailFact[] {
  const copy = { copy: true };
  const fact = (label: MessageDescriptor, value: Scalar | MessageDescriptor | undefined, options: FactOptions = {}): DetailFact =>
    ({ label, values: value === undefined || value === "" ? [] : [{ value, ...options }] });
  const field = (label: MessageDescriptor, path: string, options?: FactOptions) => fact(label, scalar(valueAt(obj, path)), options);
  const bytes = (label: MessageDescriptor, path: string) => {
    const value = valueAt(obj, path);
    return fact(label, typeof value === "number" ? formatBytes(value, locale) : undefined);
  };
  const gi = (label: MessageDescriptor, path: string) => {
    const value = valueAt(obj, path);
    return fact(label, typeof value === "number" ? `${formatNumber(value, locale)} GiB` : undefined);
  };
  const date = (label: MessageDescriptor, path: string) => {
    const value = text(obj, path);
    return fact(label, Number.isFinite(Date.parse(value)) ? formatDateTime(value, locale) : undefined);
  };
  const list = (label: MessageDescriptor, path: string, pick: (entry: unknown) => Scalar | undefined = scalar, options: FactOptions = {}): DetailFact => ({
    label,
    values: listAt(obj, path).flatMap((entry) => fact(label, pick(entry), options).values),
  });
  const ref = (label: MessageDescriptor, path: string, link: string) => field(label, path, { link });
  const image = () => [
    field(L.digest, "status.digest", copy), field(L.type, "spec.type"), fact(L.os, osSummary(obj)),
    bytes(L.size, "status.sizeBytes"), field(L.image, "spec.source.registry.image", copy),
    field(L.source, "spec.source.http.url", copy), ref(L.volume, "spec.source.volumeRef.name", "volumes"),
  ];
  const targetPaths: Record<string, string> = { VirtualMachine: "vms", Container: "containers", LoadBalancer: "load-balancers" };

  switch (plural) {
    case "hosts":
      return [field(L.host, "status.hostname", copy), list(L.roles, "spec.roles"), field(L.version, "status.kubernetesVersion")];
    case "clusters":
      return [field(L.version, "status.version"), field(L.desiredVersion, "spec.desiredVersion"), field(L.address, "spec.api.vip", copy), field(L.maintenance, "spec.maintenanceWindow")];
    case "releases":
      return [field(L.version, "spec.version"), field(L.k0sVersion, "spec.k0sVersion"), field(L.image, "spec.image", copy), list(L.versions, "spec.upgradeFrom")];
    case "settings":
      return [field(L.applied, "status.applied"), field(L.requested, "spec.value"), field(L.default, "status.default")];
    case "addons":
      return [fact(L.enabled, valueAt(obj, "spec.enabled") === true)];
    case "tenants":
      return [field(L.displayName, "spec.displayName"), field(L.vpcCidr, "spec.network.vpcCIDR", copy), fact(L.owners, listAt(obj, "spec.owners").length)];
    case "projects":
      return [field(L.displayName, "spec.displayName"), field(L.tenant, "spec.tenantRef"), field(L.namespace, "status.namespace", copy), fact(L.members, listAt(obj, "spec.members").length)];
    case "clusterimages":
      return [...image(), fact(L.versions, listAt(obj, "status.versions").length)];
    case "images":
      return image();
    case "networkpeerings":
      return [list(L.networks, "spec.networkRefs", scalar, copy), field(L.connectCidr, "status.connectCIDR", copy)];
    case "networks": {
      const cidrs = list(L.cidr, "spec.cidrs", scalar, copy);
      return [cidrs.values.length > 0 ? cidrs : field(L.cidr, "spec.cidr", copy), field(L.vpc, "status.vpc"), field(L.tenant, "spec.tenantRef"),
        fact(L.external, valueAt(obj, "spec.enableExternal") === true)];
    }
    case "virtualmachines":
      return [list(L.addresses, "status.addresses", (entry) => text(entry, "ip"), copy), fact(L.instanceType, vmSize(obj)), field(L.image, "spec.image.name", copy),
        field(L.node, "status.node"), list(L.volume, "spec.disks", (entry) => text(entry, "volumeRef"), { link: "volumes" })];
    case "containers": {
      const desired = valueAt(obj, "status.replicas") ?? valueAt(obj, "spec.replicas");
      return [field(L.image, "spec.image", copy), fact(L.replicas, typeof desired === "number" ? `${text(obj, "status.readyReplicas") || "0"}/${desired}` : undefined),
        list(L.addresses, "status.addresses", scalar, copy), list(L.ports, "spec.ports", (entry) => [text(entry, "protocol") || "TCP", text(entry, "port")].join(" ")),
        ref(L.subnet, "spec.network.subnetRef", "subnets")];
    }
    case "builds":
      return [field(L.resultImage, "status.image", copy), field(L.image, "spec.output.image", copy), field(L.type, "spec.type"),
        field(L.source, "spec.source.git.url", copy), field(L.revision, "spec.source.git.revision", copy), date(L.finishedAt, "status.finishedAt"),
        ref(L.subnet, "spec.network.subnetRef", "subnets")];
    case "kubernetesclusters":
      return [field(L.endpoint, "status.endpoint", copy), field(L.version, "spec.version"), ref(L.subnet, "spec.network.subnetRef", "subnets"),
        list(L.workers, "status.workers", (entry) => text(entry, "vm"), { link: "vms" })];
    case "sshkeys": {
      const key = text(obj, "spec.publicKey");
      return [fact(L.keyType, key.split(/\s+/)[0]), fact(L.publicKey, key, copy)];
    }
    case "volumes": {
      const holder = text(obj, "status.attachedTo");
      const isContainer = holder.startsWith("Container/");
      const attached = fact(L.attachedTo, isContainer ? holder.slice("Container/".length) : holder, { link: isContainer ? "containers" : "vms" });
      return [attached, gi(L.size, typeof valueAt(obj, "status.actualSizeGi") === "number" ? "status.actualSizeGi" : "spec.sizeGi"), field(L.storageClass, "spec.storageClass"),
        ref(L.source, "spec.source.snapshotRef", "snapshots"), ref(L.source, "spec.source.volumeRef", "volumes"), field(L.image, "spec.source.image.name", copy)];
    }
    case "volumesnapshots":
      return [ref(L.volume, "spec.volumeRef", "volumes"), bytes(L.size, "status.sizeBytes"), field(L.retention, "spec.retention")];
    case "virtualmachinebackups":
      return [ref(L.vm, "spec.vmRef", "vms"), date(L.lastBackup, "status.lastBackup"), field(L.target, "spec.target", copy), field(L.schedule, "spec.schedule", copy),
        field(L.keep, "spec.keep"), list(L.backupId, "status.backups", (entry) => text(entry, "id"), copy)];
    case "virtualmachinerestores":
      return [ref(L.backup, "spec.backupRef", "backups"), fact(L.backupId, text(obj, "status.backupID") || text(obj, "spec.backupID"), copy),
        field(L.targetName, "spec.targetName"), fact(L.replace, valueAt(obj, "spec.replace") === true)];
    case "subnets":
      return [field(L.cidr, "spec.cidr", copy), field(L.gateway, "spec.gateway", copy), ref(L.network, "spec.networkRef", "networks"), field(L.dhcp, "spec.dhcp"),
        ref(L.dnsZone, "spec.dnsZoneRef", "dns-zones")];
    case "elasticips": {
      const target = text(obj, "spec.target.name");
      const kind = text(obj, "spec.target.kind");
      return [field(L.address, "status.address", copy), fact(L.providerNetwork, text(obj, "status.providerNetwork") || text(obj, "spec.providerNetwork")),
        fact(L.target, target, { link: targetPaths[kind] ?? "" }), ref(L.subnet, "spec.target.subnetRef", "subnets")];
    }
    case "loadbalancers":
      return [field(L.address, "status.address", copy), field(L.internalAddress, "status.internalAddress", copy), ref(L.elasticIp, "spec.elasticIPRef", "elastic-ips"),
        list(L.listeners, "spec.listeners", listener)];
    case "internetgateways":
      return [ref(L.elasticIp, "spec.elasticIPRef", "elastic-ips"), ref(L.network, "spec.networkRef", "networks"), ref(L.subnet, "spec.subnetRef", "subnets")];
    case "dnszones":
      return [field(L.zone, "spec.zone", copy), list(L.records, "spec.records", (entry) => {
        const name = text(entry, "name");
        return name ? `${name}: ${nestedList(entry, "ips").map(scalar).filter(Boolean).join(", ")}` : undefined;
      })];
    case "securitygroups":
      return [fact(L.ingress, listAt(obj, "spec.ingress").length), fact(L.egress, listAt(obj, "spec.egress").length)];
    case "objectstoragebuckets":
      return [field(L.endpoint, "status.endpoint", copy), fact(L.bucketName, text(obj, "status.bucketName") || text(obj, "spec.bucketName"), copy),
        bytes(L.usage, "status.usage.bytes"), field(L.objects, "status.usage.objects"), fact(L.quota, formatQuantity(text(obj, "spec.quota.bytes"))),
        fact(L.versioning, valueAt(obj, "spec.versioning") === true)];
    case "objectstorageusers":
      return [field(L.displayName, "spec.displayName"), field(L.permission, "spec.permission"), list(L.buckets, "spec.buckets", scalar, { link: "buckets" }),
        fact(L.suspended, valueAt(obj, "spec.suspended") === true)];
    case "databases":
      return [list(L.addressPort, "status.addresses", (entry) => addressPort(text(entry, "ip"), text(entry, "port")), copy),
        field(L.proxyAddress, "status.proxyAddress", copy), ref(L.subnet, "spec.subnetRef", "subnets"), field(L.engine, "spec.engine"),
        field(L.version, "spec.version"), field(L.class, "spec.class"), gi(L.size, "spec.storageGi"), field(L.instances, "status.instances"),
        fact(L.backupsEnabled, valueAt(obj, "spec.backup.enabled") === true), date(L.lastBackup, "status.backup.lastAt")];
    case "databaseusers":
      return [ref(L.database, "spec.databaseRef", "databases"), fact(L.username, text(obj, "status.username") || text(obj, "spec.username"), copy),
        list(L.grants, "spec.grants", (entry) => [text(entry, "database"), text(entry, "level")].filter(Boolean).join(" · "))];
    case "databaseschemas":
      return [ref(L.database, "spec.databaseRef", "databases"), fact(L.databaseName, text(obj, "status.name") || text(obj, "spec.name"), copy), field(L.policy, "spec.deletionPolicy")];
    case "databaseversions":
      return [field(L.engine, "spec.engine"), field(L.version, "spec.major"), field(L.image, "spec.image", copy), field(L.port, "spec.port", copy),
        fact(L.enabled, valueAt(obj, "spec.enabled") === true), field(L.eolDate, "spec.eolDate")];
    case "databaseclasses":
      return [fact(L.cpu, formatQuantity(text(obj, "spec.cpu"))), fact(L.memory, formatQuantity(text(obj, "spec.memory"))),
        gi(L.minStorage, "spec.minStorageGi"), gi(L.maxStorage, "spec.maxStorageGi"), fact(L.highAvailability, valueAt(obj, "spec.highAvailability") === true),
        fact(L.enabled, valueAt(obj, "spec.enabled") === true)];
    case "registries":
      return [field(L.host, "status.host", copy), fact(L.project, text(obj, "status.projectName") || text(obj, "spec.name"), copy),
        bytes(L.usage, "status.usedBytes"), valueAt(obj, "spec.quotaGB") === 0 ? fact(L.quota, L.unlimited) : gi(L.quota, "spec.quotaGB"),
        field(L.repositories, "status.repositoryCount")];
    case "registrycredentials":
      return [ref(L.registry, "spec.registryRef.name", "registries"), field(L.username, "status.username", copy), field(L.permission, "spec.permission")];
    default:
      return [];
  }
}
