import { msg } from "@lingui/core/macro";
import type { MessageDescriptor } from "@lingui/core";
import type { ResourceRef } from "../api/kube";
import type { KubeObject } from "../api/types";
import { bedrockGroup } from "../stream/protocol";
import { columnsByPlural, type ColumnDef } from "./columns";
import type { UiSchema } from "@rjsf/utils";
import { actionStates, type VmAction } from "./vmActionStates";
import { validateBackup, validateBucket, validateDnsZone, validateSecurityGroup, validateSshKey, type SpecValidator } from "./validators";

export type Section = "Infrastructure" | "Platform" | "Tenancy" | "Catalog" | "Compute" | "Storage" | "Networking" | "Object storage" | "Databases" | "Registry";
export type Perspective = "admin" | "project";

export interface ActionDef {
  id: string;
  label: MessageDescriptor;
  kind: "subresource" | "patch" | "delete";
  verb: string;
  subresource?: string;
  enabledWhen: (object: KubeObject) => boolean;
  confirm: "none" | "simple" | "typed";
  patch?: (object: KubeObject) => unknown;
}

export interface ResourceDef {
  kind: string;
  group: string;
  version: string;
  plural: string;
  scope: "Namespaced" | "Cluster";
  perspective: Perspective;
  section: Section;
  path: string;
  title: MessageDescriptor;
  columns: ColumnDef[];
  actions: ActionDef[];
  create: boolean;
  edit: boolean;
  createPath?: string;
  validate?: SpecValidator;
  uiSchema?: UiSchema;
  refFields?: Record<string, string>;
}

const always = () => true;

const del = (confirm: "simple" | "typed"): ActionDef => ({
  id: "delete",
  label: msg({ id: "common.action.delete", message: "Delete" }),
  kind: "delete",
  verb: "delete",
  enabledWhen: always,
  confirm,
});

function vmAction(id: Exclude<VmAction, "delete">, label: MessageDescriptor, confirm: "none" | "simple"): ActionDef {
  return {
    id,
    label,
    kind: "subresource",
    verb: "update",
    subresource: id,
    enabledWhen: (object) => actionStates(object)[id] === "enabled",
    confirm,
  };
}

const containerRestart: ActionDef = {
  id: "restart",
  label: msg({ id: "action.restart", message: "Restart" }),
  kind: "subresource",
  verb: "update",
  subresource: "restart",
  enabledWhen: (object) => object.status?.phase === "Running",
  confirm: "simple",
};

const vmDelete: ActionDef = { ...del("typed"), enabledWhen: (object) => actionStates(object).delete === "enabled" };

const vmActions: ActionDef[] = [
  vmAction("start", msg({ id: "action.start", message: "Start" }), "none"),
  vmAction("stop", msg({ id: "action.stop", message: "Stop" }), "simple"),
  vmAction("restart", msg({ id: "action.restart", message: "Restart" }), "simple"),
  vmAction("migrate", msg({ id: "action.migrate", message: "Migrate" }), "none"),
  vmAction("snapshot", msg({ id: "action.snapshot", message: "Snapshot" }), "none"),
  vmDelete,
];

interface Row {
  kind: string;
  plural: string;
  scope: "Namespaced" | "Cluster";
  perspective: Perspective;
  section: Section;
  path: string;
  title: MessageDescriptor;
  create: boolean;
  edit: boolean;
  actions: ActionDef[];
  createPath?: string;
  validate?: SpecValidator;
  uiSchema?: UiSchema;
  refFields?: Record<string, string>;
}

const t = {
  hosts: msg({ id: "nav.item.hosts", message: "Hosts" }),
  upgrades: msg({ id: "nav.item.upgrades", message: "Upgrades" }),
  settings: msg({ id: "nav.item.settings", message: "Settings" }),
  addons: msg({ id: "nav.item.addons", message: "Addons" }),
  tenants: msg({ id: "nav.item.tenants", message: "Tenants" }),
  clusterImages: msg({ id: "nav.item.clusterImages", message: "Catalog images" }),
  vms: msg({ id: "nav.item.vms", message: "Virtual machines" }),
  containers: msg({ id: "nav.item.containers", message: "Containers" }),
  builds: msg({ id: "nav.item.builds", message: "Builds" }),
  kubernetesClusters: msg({ id: "nav.item.kubernetesClusters", message: "Kubernetes clusters" }),
  sshKeys: msg({ id: "nav.item.sshKeys", message: "SSH keys" }),
  volumes: msg({ id: "nav.item.volumes", message: "Volumes" }),
  snapshots: msg({ id: "nav.item.snapshots", message: "Snapshots" }),
  images: msg({ id: "nav.item.images", message: "Images" }),
  backups: msg({ id: "nav.item.backups", message: "Backups" }),
  networkPeerings: msg({ id: "nav.item.networkPeerings", message: "Network peerings" }),
  networks: msg({ id: "nav.item.networks", message: "Networks" }),
  subnets: msg({ id: "nav.item.subnets", message: "Subnets" }),
  elasticIps: msg({ id: "nav.item.elasticIps", message: "Elastic IPs" }),
  loadBalancers: msg({ id: "nav.item.loadBalancers", message: "Load balancers" }),
  internetGateways: msg({ id: "nav.item.internetGateways", message: "Internet gateways" }),
  dnsZones: msg({ id: "nav.item.dnsZones", message: "DNS zones" }),
  securityGroups: msg({ id: "nav.item.securityGroups", message: "Security groups" }),
  buckets: msg({ id: "nav.item.buckets", message: "Buckets" }),
  objectUsers: msg({ id: "nav.item.objectUsers", message: "Object storage users" }),
  databases: msg({ id: "nav.item.databases", message: "Databases" }),
  databaseUsers: msg({ id: "nav.item.databaseUsers", message: "Database users" }),
  databaseSchemas: msg({ id: "nav.item.databaseSchemas", message: "Database schemas" }),
  databaseVersions: msg({ id: "nav.item.databaseVersions", message: "Database versions" }),
  databaseClasses: msg({ id: "nav.item.databaseClasses", message: "Database classes" }),
  registries: msg({ id: "nav.item.registries", message: "Registries" }),
  registryCredentials: msg({ id: "nav.item.registryCredentials", message: "Registry credentials" }),
};

function row(r: Omit<Row, "actions" | "createPath" | "validate" | "uiSchema" | "refFields"> & { actions?: ActionDef[]; createPath?: string; validate?: SpecValidator; uiSchema?: UiSchema; refFields?: Record<string, string> }): Row {
  return { actions: [], ...r };
}

const rows: Row[] = [
  row({ kind: "Host", plural: "hosts", scope: "Cluster", perspective: "admin", section: "Infrastructure", path: "hosts", title: t.hosts, create: false, edit: true }),
  row({ kind: "Cluster", plural: "clusters", scope: "Cluster", perspective: "admin", section: "Infrastructure", path: "upgrades", title: t.upgrades, create: false, edit: true }),
  row({ kind: "Release", plural: "releases", scope: "Cluster", perspective: "admin", section: "Infrastructure", path: "upgrades", title: t.upgrades, create: false, edit: false }),
  row({ kind: "NetworkPeering", plural: "networkpeerings", scope: "Cluster", perspective: "admin", section: "Infrastructure", path: "network-peerings", title: t.networkPeerings, create: true, edit: false, actions: [del("typed")] }),
  row({ kind: "Setting", plural: "settings", scope: "Cluster", perspective: "admin", section: "Platform", path: "settings", title: t.settings, create: false, edit: true }),
  row({ kind: "Addon", plural: "addons", scope: "Cluster", perspective: "admin", section: "Platform", path: "addons", title: t.addons, create: false, edit: true }),
  row({ kind: "Tenant", plural: "tenants", scope: "Cluster", perspective: "admin", section: "Tenancy", path: "tenants", title: t.tenants, create: true, edit: true, actions: [del("typed")] }),
  row({ kind: "Project", plural: "projects", scope: "Cluster", perspective: "admin", section: "Tenancy", path: "tenants", title: t.tenants, create: true, edit: true, actions: [del("typed")] }),
  row({ kind: "ClusterImage", plural: "clusterimages", scope: "Cluster", perspective: "admin", section: "Catalog", path: "cluster-images", title: t.clusterImages, create: true, edit: true, actions: [del("simple")] }),
  row({ kind: "DatabaseVersion", plural: "databaseversions", scope: "Cluster", perspective: "admin", section: "Catalog", path: "database-versions", title: t.databaseVersions, create: true, edit: true, actions: [del("simple")] }),
  row({ kind: "DatabaseClass", plural: "databaseclasses", scope: "Cluster", perspective: "admin", section: "Catalog", path: "database-classes", title: t.databaseClasses, create: true, edit: true, actions: [del("simple")] }),
  row({ kind: "Network", plural: "networks", scope: "Cluster", perspective: "project", section: "Networking", path: "networks", title: t.networks, create: true, edit: true, actions: [del("typed")] }),
  row({ kind: "VirtualMachine", plural: "virtualmachines", scope: "Namespaced", perspective: "project", section: "Compute", path: "vms", title: t.vms, create: true, edit: true, actions: vmActions, createPath: "vms/new" }),
  row({ kind: "Container", plural: "containers", scope: "Namespaced", perspective: "project", section: "Compute", path: "containers", title: t.containers, create: true, edit: true, actions: [containerRestart, del("simple")], refFields: { "network.subnetRef": "subnets", "network.securityGroups.*": "securitygroups", "volumes.*.volumeRef": "volumes" } }),
  row({ kind: "Build", plural: "builds", scope: "Namespaced", perspective: "project", section: "Compute", path: "builds", title: t.builds, create: true, edit: false, actions: [del("simple")], refFields: { "network.subnetRef": "subnets" } }),
  row({ kind: "KubernetesCluster", plural: "kubernetesclusters", scope: "Namespaced", perspective: "project", section: "Compute", path: "kubernetes-clusters", title: t.kubernetesClusters, create: true, edit: true, actions: [del("typed")], refFields: { "network.subnetRef": "subnets", "controlPlane.securityGroups.*": "securitygroups", "workers.*.securityGroups.*": "securitygroups", "workers.*.image.name": "clusterimages" } }),
  row({ kind: "SSHKey", plural: "sshkeys", scope: "Namespaced", perspective: "project", section: "Compute", path: "ssh-keys", title: t.sshKeys, create: true, edit: false, actions: [del("simple")], validate: validateSshKey, uiSchema: { publicKey: { "ui:widget": "textarea" } } }),
  row({ kind: "Volume", plural: "volumes", scope: "Namespaced", perspective: "project", section: "Storage", path: "volumes", title: t.volumes, create: true, edit: true, actions: [del("typed")] }),
  row({ kind: "VolumeSnapshot", plural: "volumesnapshots", scope: "Namespaced", perspective: "project", section: "Storage", path: "snapshots", title: t.snapshots, create: true, edit: false, actions: [del("simple")] }),
  row({ kind: "Image", plural: "images", scope: "Namespaced", perspective: "project", section: "Storage", path: "images", title: t.images, create: true, edit: false, actions: [del("simple")] }),
  row({ kind: "VirtualMachineBackup", plural: "virtualmachinebackups", scope: "Namespaced", perspective: "project", section: "Storage", path: "backups", title: t.backups, create: true, edit: true, actions: [del("simple")], validate: validateBackup }),
  row({ kind: "VirtualMachineRestore", plural: "virtualmachinerestores", scope: "Namespaced", perspective: "project", section: "Storage", path: "backups", title: t.backups, create: false, edit: false, actions: [del("simple")] }),
  row({ kind: "Subnet", plural: "subnets", scope: "Namespaced", perspective: "project", section: "Networking", path: "subnets", title: t.subnets, create: true, edit: true, actions: [del("simple")], refFields: { networkRef: "networks" } }),
  row({ kind: "ElasticIP", plural: "elasticips", scope: "Namespaced", perspective: "project", section: "Networking", path: "elastic-ips", title: t.elasticIps, create: true, edit: true, actions: [del("typed")] }),
  row({ kind: "LoadBalancer", plural: "loadbalancers", scope: "Namespaced", perspective: "project", section: "Networking", path: "load-balancers", title: t.loadBalancers, create: true, edit: true, actions: [del("simple")], refFields: { "listeners.*.targets.*.vmRef": "virtualmachines", "listeners.*.targets.*.containerRef": "containers" }, uiSchema: { elasticIPRef: { "ui:widget": "hidden" } } }),
  row({ kind: "InternetGateway", plural: "internetgateways", scope: "Namespaced", perspective: "project", section: "Networking", path: "internet-gateways", title: t.internetGateways, create: true, edit: true, actions: [del("simple")], refFields: { networkRef: "networks", elasticIPRef: "elasticips", subnetRef: "subnets" } }),
  row({ kind: "DNSZone", plural: "dnszones", scope: "Namespaced", perspective: "project", section: "Networking", path: "dns-zones", title: t.dnsZones, create: true, edit: true, actions: [del("simple")], validate: validateDnsZone }),
  row({ kind: "SecurityGroup", plural: "securitygroups", scope: "Namespaced", perspective: "project", section: "Networking", path: "security-groups", title: t.securityGroups, create: true, edit: true, actions: [del("simple")], validate: validateSecurityGroup }),
  row({ kind: "ObjectStorageBucket", plural: "objectstoragebuckets", scope: "Namespaced", perspective: "project", section: "Object storage", path: "buckets", title: t.buckets, create: true, edit: true, actions: [del("typed")], validate: validateBucket, uiSchema: { policy: { "ui:widget": "textarea" } } }),
  row({ kind: "Database", plural: "databases", scope: "Namespaced", perspective: "project", section: "Databases", path: "databases", title: t.databases, create: true, edit: true, actions: [del("typed")], refFields: { subnetRef: "subnets" } }),
  row({ kind: "DatabaseUser", plural: "databaseusers", scope: "Namespaced", perspective: "project", section: "Databases", path: "database-users", title: t.databaseUsers, create: true, edit: true, actions: [del("typed")], refFields: { databaseRef: "databases" } }),
  row({ kind: "DatabaseSchema", plural: "databaseschemas", scope: "Namespaced", perspective: "project", section: "Databases", path: "database-schemas", title: t.databaseSchemas, create: true, edit: true, actions: [del("typed")], refFields: { databaseRef: "databases" } }),
  row({ kind: "ObjectStorageUser", plural: "objectstorageusers", scope: "Namespaced", perspective: "project", section: "Object storage", path: "object-users", title: t.objectUsers, create: true, edit: true, actions: [del("typed")] }),
  row({ kind: "Registry", plural: "registries", scope: "Namespaced", perspective: "project", section: "Registry", path: "registries", title: t.registries, create: true, edit: true, actions: [del("typed")] }),
  row({ kind: "RegistryCredential", plural: "registrycredentials", scope: "Namespaced", perspective: "project", section: "Registry", path: "registry-credentials", title: t.registryCredentials, create: true, edit: false, actions: [del("simple")], refFields: { "registryRef.name": "registries" } }),
];

export const definitions: ResourceDef[] = rows.map((r) => ({
  kind: r.kind,
  group: bedrockGroup,
  version: "v1alpha1",
  plural: r.plural,
  scope: r.scope,
  perspective: r.perspective,
  section: r.section,
  path: r.path,
  title: r.title,
  columns: columnsByPlural[r.plural] ?? [],
  actions: r.actions,
  create: r.create,
  edit: r.edit,
  ...(r.createPath === undefined ? {} : { createPath: r.createPath }),
  ...(r.validate === undefined ? {} : { validate: r.validate }),
  ...(r.uiSchema === undefined ? {} : { uiSchema: r.uiSchema }),
  ...(r.refFields === undefined ? {} : { refFields: r.refFields }),
}));

export const notShown: readonly string[] = ["HostConfig", "ClusterConfig", "NodeUpgrade"];

export const authnKinds: readonly string[] = [
  "APIToken",
  "AuthCode",
  "AuthRequest",
  "Credential",
  "DeviceRequest",
  "Group",
  "IdentityProvider",
  "OAuthClient",
  "RefreshToken",
  "Session",
  "SigningKey",
  "User",
];

export function definitionFor(plural: string): ResourceDef | undefined {
  return definitions.find((def) => def.plural === plural);
}

export function definitionsFor(perspective: Perspective): ResourceDef[] {
  return definitions.filter((def) => def.perspective === perspective);
}

export function primaryDefinitionsFor(perspective: Perspective): ResourceDef[] {
  return definitionsFor(perspective).filter((def, index, all) => all.findIndex((other) => other.path === def.path) === index);
}

export function refOf(def: ResourceDef): ResourceRef {
  return { group: def.group, version: def.version, plural: def.plural, namespaced: def.scope === "Namespaced" };
}
