import { msg } from "@lingui/core/macro";
import type { MessageDescriptor } from "@lingui/core";
import type { KubeObject } from "../../api/types";

export type SettingKind = "text" | "boolean" | "enum" | "multiline";

export interface SettingMeta {
  kind: SettingKind;
  options: string[];
  description: MessageDescriptor | undefined;
}

const booleanKeys = new Set(["kata.enabled", "loki.enabled", "authn.require-second-factor", "virt.emulation"]);
const multilineKeys = new Set(["platform.additional-ca", "backup.archive-recipients"]);
const enums: Record<string, string[]> = {
  "platform.tls-mode": ["SelfSigned", "LetsEncrypt", "Custom"],
  "letsencrypt.solver": ["http01", "dns01"],
  "audit.level": ["Metadata", "RequestResponse"],
  "network.eip-mode": ["arp", "bgp"],
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
  const kind: SettingKind = booleanKeys.has(key) ? "boolean" : multilineKeys.has(key) ? "multiline" : enums[key] ? "enum" : "text";
  return { kind, options: enums[key] ?? [], description: descriptions[key] };
}

export const settingGroupOrder = ["platform", "letsencrypt", "network", "storage", "migration", "overcommit", "virt", "backup", "audit", "alertmanager", "loki", "kata", "images", "authn"];

export function groupSettings(settings: readonly KubeObject[]): { prefix: string; items: KubeObject[] }[] {
  const prefixOf = (setting: KubeObject) => setting.metadata.name.split(".")[0] ?? "";
  const prefixes = [...new Set(settings.map(prefixOf))];
  const ordered = [...settingGroupOrder.filter((prefix) => prefixes.includes(prefix)), ...prefixes.filter((prefix) => !settingGroupOrder.includes(prefix)).sort()];
  return ordered.map((prefix) => ({
    prefix,
    items: settings.filter((setting) => prefixOf(setting) === prefix).sort((left, right) => left.metadata.name.localeCompare(right.metadata.name)),
  }));
}
