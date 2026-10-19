import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const doc = readFileSync(resolve(import.meta.dirname, "../../../../docs/panel.md"), "utf8");

const routes = [
  "/admin/hosts",
  "/admin/hosts/:name",
  "/admin/settings",
  "/admin/upgrades",
  "/admin/network-peerings",
  "/admin/tenants",
  "/admin/tenants/:name",
  "/admin/cluster-images",
  "/admin/cluster-images/:name",
  "/admin/addons",
  "/admin/database-versions",
  "/admin/database-classes",
  "/p/:project/vms",
  "/p/:project/vms/new",
  "/p/:project/vms/new-iso",
  "/p/:project/vms/:name",
  "/p/:project/volumes",
  "/p/:project/volumes/:name",
  "/p/:project/snapshots",
  "/p/:project/images",
  "/p/:project/images/:name",
  "/p/:project/backups",
  "/p/:project/ssh-keys",
  "/p/:project/networks",
  "/p/:project/subnets",
  "/p/:project/elastic-ips",
  "/p/:project/load-balancers",
  "/p/:project/internet-gateways",
  "/p/:project/dns-zones",
  "/p/:project/security-groups",
  "/p/:project/databases",
  "/p/:project/database-users",
  "/p/:project/database-schemas",
  "/p/:project/buckets",
  "/p/:project/object-users",
  "/account",
];

const queries = [
  "vm.cpu",
  "vm.memory",
  "vm.network.rx",
  "vm.network.tx",
  "vm.disk.read",
  "vm.disk.write",
  "volume.used",
  "volume.capacity",
  "host.cpu",
  "host.memory",
  "host.network.rx",
  "host.network.tx",
  "ceph.used",
  "ceph.capacity",
];

describe("docs/panel.md", () => {
  it("TestDocNamesEveryRouteAndQuery", () => {
    for (const route of routes) {
      expect(doc, route).toContain(`\`${route}\``);
    }
    for (const query of queries) {
      expect(doc, query).toContain(`\`${query}\``);
    }
  });
});
