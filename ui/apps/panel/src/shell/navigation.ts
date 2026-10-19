import { msg } from "@lingui/core/macro";
import type { MessageDescriptor } from "@lingui/core";

export type Perspective = "admin" | "project";

export interface NavItem {
  id: string;
  label: MessageDescriptor;
  to: string;
}

export interface NavSection {
  id: string;
  label: MessageDescriptor;
  items: NavItem[];
}

function item(id: string, label: MessageDescriptor, base: string): NavItem {
  return { id, label, to: `${base}/${id}` };
}

function adminNavigation(): NavSection[] {
  return [
    {
      id: "infrastructure",
      label: msg({ id: "nav.section.infrastructure", message: "Infrastructure" }),
      items: [item("hosts", msg({ id: "nav.item.hosts", message: "Hosts" }), "/admin"), item("upgrades", msg({ id: "nav.item.upgrades", message: "Upgrades" }), "/admin")],
    },
    {
      id: "platform",
      label: msg({ id: "nav.section.platform", message: "Platform" }),
      items: [item("settings", msg({ id: "nav.item.settings", message: "Settings" }), "/admin"), item("addons", msg({ id: "nav.item.addons", message: "Addons" }), "/admin")],
    },
    {
      id: "tenancy",
      label: msg({ id: "nav.section.tenancy", message: "Tenancy" }),
      items: [item("tenants", msg({ id: "nav.item.tenants", message: "Tenants" }), "/admin")],
    },
    {
      id: "catalog",
      label: msg({ id: "nav.section.catalog", message: "Catalog" }),
      items: [item("cluster-images", msg({ id: "nav.item.clusterImages", message: "Catalog images" }), "/admin")],
    },
  ];
}

function projectNavigation(project: string): NavSection[] {
  const base = `/p/${project}`;
  return [
    {
      id: "compute",
      label: msg({ id: "nav.section.compute", message: "Compute" }),
      items: [item("vms", msg({ id: "nav.item.vms", message: "Virtual machines" }), base), item("containers", msg({ id: "nav.item.containers", message: "Containers" }), base), item("builds", msg({ id: "nav.item.builds", message: "Builds" }), base), item("ssh-keys", msg({ id: "nav.item.sshKeys", message: "SSH keys" }), base)],
    },
    {
      id: "storage",
      label: msg({ id: "nav.section.storage", message: "Storage" }),
      items: [
        item("volumes", msg({ id: "nav.item.volumes", message: "Volumes" }), base),
        item("snapshots", msg({ id: "nav.item.snapshots", message: "Snapshots" }), base),
        item("images", msg({ id: "nav.item.images", message: "Images" }), base),
        item("backups", msg({ id: "nav.item.backups", message: "Backups" }), base),
      ],
    },
    {
      id: "networking",
      label: msg({ id: "nav.section.networking", message: "Networking" }),
      items: [
        item("networks", msg({ id: "nav.item.networks", message: "Networks" }), base),
        item("subnets", msg({ id: "nav.item.subnets", message: "Subnets" }), base),
        item("elastic-ips", msg({ id: "nav.item.elasticIps", message: "Elastic IPs" }), base),
        item("load-balancers", msg({ id: "nav.item.loadBalancers", message: "Load balancers" }), base),
        item("internet-gateways", msg({ id: "nav.item.internetGateways", message: "Internet gateways" }), base),
        item("dns-zones", msg({ id: "nav.item.dnsZones", message: "DNS zones" }), base),
        item("security-groups", msg({ id: "nav.item.securityGroups", message: "Security groups" }), base),
      ],
    },
    {
      id: "objectStorage",
      label: msg({ id: "nav.section.objectStorage", message: "Object storage" }),
      items: [
        item("buckets", msg({ id: "nav.item.buckets", message: "Buckets" }), base),
        item("object-users", msg({ id: "nav.item.objectUsers", message: "Object storage users" }), base),
      ],
    },
  ];
}

export function navigationFor(perspective: Perspective, project: string): NavSection[] {
  return perspective === "admin" ? adminNavigation() : projectNavigation(project);
}
