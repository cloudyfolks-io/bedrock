import { msg } from "@lingui/core/macro";
import type { MessageDescriptor } from "@lingui/core";
import type { KubeObject } from "../../api/types";

export type SettingKind = "text" | "boolean" | "enum" | "multiline";

export interface SettingMeta {
  kind: SettingKind;
  options: string[];
  label: MessageDescriptor | undefined;
  description: MessageDescriptor | undefined;
}

const booleanKeys = new Set(["kata.enabled", "loki.enabled", "authn.require-second-factor", "virt.emulation"]);
const multilineKeys = new Set(["platform.additional-ca", "backup.archive-recipients"]);
const enums: Record<string, string[]> = {
  "platform.tls-mode": ["SelfSigned", "LetsEncrypt", "Custom"],
  "letsencrypt.solver": ["http01", "dns01"],
  "audit.level": ["Metadata", "RequestResponse"],
  "network.eip-mode": ["l2", "bgp"],
};

const labels: Record<string, MessageDescriptor> = {
  "platform.host": msg({ id: "settings.label.platformHost", message: "Platform hostname" }),
  "platform.tls-mode": msg({ id: "settings.label.tlsMode", message: "TLS certificate source" }),
  "platform.additional-ca": msg({ id: "settings.label.additionalCa", message: "Additional trusted certificates" }),
  "platform.custom-tls": msg({ id: "settings.label.customTls", message: "Custom TLS secret" }),
  "letsencrypt.email": msg({ id: "settings.label.letsencryptEmail", message: "Account email" }),
  "letsencrypt.solver": msg({ id: "settings.label.letsencryptSolver", message: "ACME challenge solver" }),
  "storage.replicas": msg({ id: "settings.label.storageReplicas", message: "Ceph replicas" }),
  "storage.network": msg({ id: "settings.label.storageNetwork", message: "Storage network VLAN" }),
  "migration.network": msg({ id: "settings.label.migrationNetwork", message: "Migration network VLAN" }),
  "migration.parallel": msg({ id: "settings.label.migrationParallel", message: "Parallel live migrations" }),
  "virt.emulation": msg({ id: "settings.label.virtEmulation", message: "Software emulation" }),
  "overcommit.cpu": msg({ id: "settings.label.overcommitCpu", message: "CPU overcommit ratio" }),
  "overcommit.memory": msg({ id: "settings.label.overcommitMemory", message: "Memory overcommit ratio" }),
  "backup.target": msg({ id: "settings.label.backupTarget", message: "Backup destination" }),
  "backup.credentials-secret": msg({ id: "settings.label.backupCredentials", message: "Backup credentials secret" }),
  "backup.etcd-schedule": msg({ id: "settings.label.backupEtcd", message: "etcd snapshot schedule" }),
  "backup.archive-recipients": msg({ id: "settings.label.backupArchiveRecipients", message: "Archive encryption recipients" }),
  "audit.level": msg({ id: "settings.label.auditLevel", message: "Read request audit level" }),
  "loki.retention-days": msg({ id: "settings.label.lokiRetention", message: "Log retention (days)" }),
  "kata.enabled": msg({ id: "settings.label.kataEnabled", message: "Enable Kata at install" }),
  "loki.enabled": msg({ id: "settings.label.lokiEnabled", message: "Enable Loki at install" }),
  "images.refresh-schedule": msg({ id: "settings.label.imagesRefresh", message: "Image refresh schedule" }),
  "images.keep": msg({ id: "settings.label.imagesKeep", message: "Image versions to keep" }),
  "authn.require-second-factor": msg({ id: "settings.label.authnSecondFactor", message: "Require a second factor" }),
  "authn.session-ttl": msg({ id: "settings.label.authnSessionTtl", message: "Session lifetime" }),
  "authn.refresh-ttl": msg({ id: "settings.label.authnRefreshTtl", message: "Refresh token lifetime" }),
  "authn.lockout-threshold": msg({ id: "settings.label.authnLockout", message: "Sign-in lockout threshold" }),
  "network.eip-mode": msg({ id: "settings.label.networkEipMode", message: "Elastic IP mode" }),
  "network.provider-networks": msg({ id: "settings.label.networkProviders", message: "Provider networks" }),
  "network.dns-servers": msg({ id: "settings.label.networkDns", message: "Tenant DNS servers" }),
  "alertmanager.webhook-url": msg({ id: "settings.label.alertWebhook", message: "Alert webhook" }),
};

const descriptions: Record<string, MessageDescriptor> = {
  "platform.host": msg({ id: "settings.desc.platformHost", message: "Public domain of the platform. Required." }),
  "platform.tls-mode": msg({ id: "settings.desc.tlsMode", message: "Certificate source for the platform endpoints." }),
  "platform.additional-ca": msg({ id: "settings.desc.additionalCa", message: "PEM bundle of extra CAs trusted for outbound connections." }),
  "platform.custom-tls": msg({ id: "settings.desc.customTls", message: "Secret name in bedrock-system holding the custom certificate." }),
  "letsencrypt.email": msg({ id: "settings.desc.letsencryptEmail", message: "Account email for Let's Encrypt." }),
  "letsencrypt.solver": msg({ id: "settings.desc.letsencryptSolver", message: "ACME challenge solver." }),
  "storage.replicas": msg({ id: "settings.desc.storageReplicas", message: "Ceph pool replica count." }),
  "storage.network": msg({ id: "settings.desc.storageNetwork", message: "VLAN id of the dedicated storage network." }),
  "migration.network": msg({ id: "settings.desc.migrationNetwork", message: "VLAN id of the dedicated live migration network." }),
  "migration.parallel": msg({ id: "settings.desc.migrationParallel", message: "Parallel live migrations per cluster." }),
  "virt.emulation": msg({ id: "settings.desc.virtEmulation", message: "Run virtual machines with software emulation on hosts without KVM." }),
  "overcommit.cpu": msg({ id: "settings.desc.overcommitCpu", message: "CPU overcommit ratio." }),
  "overcommit.memory": msg({ id: "settings.desc.overcommitMemory", message: "Memory overcommit ratio." }),
  "backup.target": msg({ id: "settings.desc.backupTarget", message: "Backup target URL, s3:// or nfs://." }),
  "backup.credentials-secret": msg({ id: "settings.desc.backupCredentials", message: "Secret name in bedrock-system holding backup credentials." }),
  "backup.etcd-schedule": msg({ id: "settings.desc.backupEtcd", message: "Cron schedule for etcd snapshots." }),
  "backup.archive-recipients": msg({ id: "settings.desc.backupArchiveRecipients", message: "Age recipients (age1...) that can open every backup archive. Separate them with spaces or new lines. Keep their identities off the cluster." }),
  "audit.level": msg({ id: "settings.desc.auditLevel", message: "Audit level for read requests." }),
  "loki.retention-days": msg({ id: "settings.desc.lokiRetention", message: "Log retention in days when Loki is enabled." }),
  "kata.enabled": msg({ id: "settings.desc.kataEnabled", message: "Kata runtime addon." }),
  "loki.enabled": msg({ id: "settings.desc.lokiEnabled", message: "Loki and Alloy addon." }),
  "images.refresh-schedule": msg({ id: "settings.desc.imagesRefresh", message: "Cron schedule for catalog image refresh." }),
  "images.keep": msg({ id: "settings.desc.imagesKeep", message: "Catalog image versions to keep." }),
  "authn.require-second-factor": msg({ id: "settings.desc.authnSecondFactor", message: "Require a second factor for every login." }),
  "authn.session-ttl": msg({ id: "settings.desc.authnSessionTtl", message: "Login session cookie lifetime." }),
  "authn.refresh-ttl": msg({ id: "settings.desc.authnRefreshTtl", message: "Refresh token lifetime." }),
  "authn.lockout-threshold": msg({ id: "settings.desc.authnLockout", message: "Failed password attempts before lockout." }),
  "network.eip-mode": msg({ id: "settings.desc.networkEipMode", message: "How elastic IPs reach the network." }),
  "network.provider-networks": msg({ id: "settings.desc.networkProviders", message: "Provider networks for elastic IPs." }),
  "network.dns-servers": msg({ id: "settings.desc.networkDns", message: "DNS servers handed to tenant subnets." }),
  "alertmanager.webhook-url": msg({ id: "settings.desc.alertWebhook", message: "Webhook that receives alerts." }),
};

export function settingMeta(key: string): SettingMeta {
  const kind: SettingKind = booleanKeys.has(key) ? "boolean" : multilineKeys.has(key) ? "multiline" : Object.hasOwn(enums, key) ? "enum" : "text";
  return { kind, options: kind === "enum" ? enums[key] ?? [] : [], label: Object.hasOwn(labels, key) ? labels[key] : undefined, description: Object.hasOwn(descriptions, key) ? descriptions[key] : undefined };
}

const groupLabels: Record<string, MessageDescriptor> = {
  platform: msg({ id: "settings.group.platform", message: "Platform access" }),
  letsencrypt: msg({ id: "settings.group.letsencrypt", message: "Let’s Encrypt" }),
  authn: msg({ id: "settings.group.authn", message: "Sign-in and sessions" }),
  network: msg({ id: "settings.group.network", message: "Networking" }),
  dns: msg({ id: "settings.group.dns", message: "DNS" }),
  egress: msg({ id: "settings.group.egress", message: "Outbound traffic" }),
  storage: msg({ id: "settings.group.storage", message: "Storage" }),
  objectstorage: msg({ id: "settings.group.objectstorage", message: "Object storage" }),
  migration: msg({ id: "settings.group.migration", message: "Live migration" }),
  overcommit: msg({ id: "settings.group.overcommit", message: "Overcommit" }),
  virt: msg({ id: "settings.group.virt", message: "Virtualization" }),
  compute: msg({ id: "settings.group.compute", message: "Compute limits" }),
  container: msg({ id: "settings.group.container", message: "Containers" }),
  clusters: msg({ id: "settings.group.clusters", message: "Kubernetes clusters" }),
  database: msg({ id: "settings.group.database", message: "Managed databases" }),
  cnpg: msg({ id: "settings.group.cnpg", message: "PostgreSQL operator" }),
  "percona-ps": msg({ id: "settings.group.perconaPs", message: "MySQL operator" }),
  registry: msg({ id: "settings.group.registry", message: "Container registry" }),
  build: msg({ id: "settings.group.build", message: "Builds" }),
  backup: msg({ id: "settings.group.backup", message: "Backups" }),
  audit: msg({ id: "settings.group.audit", message: "Audit logging" }),
  alertmanager: msg({ id: "settings.group.alertmanager", message: "Alerting" }),
  loki: msg({ id: "settings.group.loki", message: "Log collection" }),
  kata: msg({ id: "settings.group.kata", message: "Kata runtime" }),
  descheduler: msg({ id: "settings.group.descheduler", message: "Descheduler" }),
  images: msg({ id: "settings.group.images", message: "Image catalog" }),
  tenancy: msg({ id: "settings.group.tenancy", message: "Tenancy" }),
};

const settingGroupOrder = Object.keys(groupLabels);
const platformOrder = ["platform.host", "platform.tls-mode", "platform.custom-tls", "platform.additional-ca"];

export function humanizeSetting(text: string): string {
  return text.replace(/[.-]+/g, " ").replace(/^./, (first) => first.toUpperCase());
}

export function groupSettings(settings: readonly KubeObject[]): { prefix: string; label: MessageDescriptor | undefined; items: KubeObject[] }[] {
  const groups = new Map<string, KubeObject[]>();
  for (const item of settings) {
    const prefix = item.metadata.name.split(".")[0] ?? "";
    const items = groups.get(prefix);
    if (items) items.push(item);
    else groups.set(prefix, [item]);
  }
  const ordered = [...settingGroupOrder.filter((prefix) => groups.has(prefix)), ...[...groups.keys()].filter((prefix) => !Object.hasOwn(groupLabels, prefix)).sort()];
  const priority = (name: string) => platformOrder.includes(name) ? platformOrder.indexOf(name) : platformOrder.length;
  return ordered.map((prefix) => ({
    prefix,
    label: Object.hasOwn(groupLabels, prefix) ? groupLabels[prefix] : undefined,
    items: (groups.get(prefix) ?? []).sort((left, right) => priority(left.metadata.name) - priority(right.metadata.name) || left.metadata.name.localeCompare(right.metadata.name)),
  }));
}
