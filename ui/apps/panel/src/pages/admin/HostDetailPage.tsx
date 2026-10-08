import { lazy, Suspense, useState, type ReactNode } from "react";
import { useParams, useSearchParams } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { Badge, Button, Switch, Tab, TabList, TabPanel, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow, Tabs, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { conditionOf, kubeClient, listKey, objectKey } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { ConfirmSimpleDialog } from "../../components/ConfirmDialogs";
import { EmptyState } from "../../components/EmptyState";
import { Ltr } from "../../components/Ltr";
import { PageHeader } from "../../components/PageHeader";
import { Spinner } from "../../components/Spinner";
import { LazyEditDialog } from "../../forms/LazyDialogs";
import { formatBytes } from "../../format";
import { useLocale } from "../../LocaleProvider";
import { listAt, textAt } from "../../resources/path";
import { definitionFor, refOf } from "../../resources/registry";
import { useLiveObject } from "../../stream/live";
import { hostSummary } from "./hostSummary";

const HostMetrics = lazy(() => import("../../charts/HostMetrics"));

const conditionTypes = ["Ready", "ManagementApplied", "MaintenanceMode", "RebootPending"];

interface Disk { path: string; model?: string; serial?: string; sizeBytes: number; inUseBy?: string }
interface Nic { name: string; mac: string; speedMbps?: number; link: boolean; pciAddress?: string }
interface Pci { address: string; vendor: string; device: string; class: string; driver?: string }
interface Step { name: string; state: string; message?: string }

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="br-section">
      <h2>{title}</h2>
      {children}
    </section>
  );
}

function Overview({ host }: { host: KubeObject }) {
  const { t } = useLingui();
  const summary = hostSummary(host);
  return (
    <>
      <dl className="br-facts">
        <dt>{t({ id: "hostDetail.roles", message: "Roles" })}</dt>
        <dd>{summary.roles.map((role) => <Badge key={role} tone="neutral">{role}</Badge>)}</dd>
        <dt>{t({ id: "hostDetail.window", message: "Maintenance window" })}</dt>
        <dd>{textAt(host, "spec.maintenanceWindow") || "—"}</dd>
        <dt>{t({ id: "hostDetail.version", message: "Kubernetes version" })}</dt>
        <dd><Ltr>{textAt(host, "status.kubernetesVersion") || "—"}</Ltr></dd>
      </dl>
      <Table caption={t({ id: "detail.conditions.title", message: "Conditions" })}>
        <TableHead>
          <TableRow>
            <TableHeaderCell>{t({ id: "detail.conditions.type", message: "Type" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "detail.conditions.status", message: "Status" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "detail.conditions.message", message: "Message" })}</TableHeaderCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {conditionTypes.map((type) => {
            const condition = conditionOf(host, type);
            return (
              <TableRow key={type}>
                <TableCell><Ltr>{type}</Ltr></TableCell>
                <TableCell>{condition ? <Badge tone={condition.status === "True" ? "success" : condition.status === "False" ? "neutral" : "warning"}>{condition.status}</Badge> : "—"}</TableCell>
                <TableCell>{condition?.message ?? ""}</TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
      <Section title={t({ id: "hostDetail.applied", message: "Applied configuration" })}>
        <Table caption={t({ id: "hostDetail.applied", message: "Applied configuration" })}>
          <TableHead>
            <TableRow>
              <TableHeaderCell>{t({ id: "hostDetail.step", message: "Step" })}</TableHeaderCell>
              <TableHeaderCell>{t({ id: "detail.conditions.status", message: "Status" })}</TableHeaderCell>
              <TableHeaderCell>{t({ id: "detail.conditions.message", message: "Message" })}</TableHeaderCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {(listAt(host, "status.applied.steps") as Step[]).map((step) => (
              <TableRow key={step.name}>
                <TableCell><Ltr>{step.name}</Ltr></TableCell>
                <TableCell>{step.state}</TableCell>
                <TableCell>{step.message ?? ""}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Section>
    </>
  );
}

function Inventory({ host }: { host: KubeObject }) {
  const { t } = useLingui();
  const { locale } = useLocale();
  const memory = hostSummary(host).memory;
  return (
    <>
      <Section title={t({ id: "hostDetail.cpu", message: "CPU" })}>
        <dl className="br-facts">
          <dt>{t({ id: "hostDetail.cpuModel", message: "Model" })}</dt>
          <dd><Ltr>{textAt(host, "status.inventory.cpu.model") || "—"}</Ltr></dd>
          <dt>{t({ id: "hostDetail.cores", message: "Cores" })}</dt>
          <dd>{textAt(host, "status.inventory.cpu.cores")}</dd>
          <dt>{t({ id: "hostDetail.threads", message: "Threads" })}</dt>
          <dd>{textAt(host, "status.inventory.cpu.threads")}</dd>
          <dt>{t({ id: "hostDetail.sockets", message: "Sockets" })}</dt>
          <dd>{textAt(host, "status.inventory.cpu.sockets")}</dd>
          <dt>{t({ id: "hostDetail.memory", message: "Memory" })}</dt>
          <dd>{formatBytes(memory, locale)}</dd>
        </dl>
      </Section>
      <Section title={t({ id: "hostDetail.disks", message: "Disks" })}>
        <Table caption={t({ id: "hostDetail.disks", message: "Disks" })}>
          <TableHead>
            <TableRow>
              {["path", "model", "serial", "size", "inUseBy"].map((key) => (
                <TableHeaderCell key={key}>{{ path: t({ id: "hostDetail.disk.path", message: "Path" }), model: t({ id: "hostDetail.cpuModel", message: "Model" }), serial: t({ id: "hostDetail.disk.serial", message: "Serial" }), size: t({ id: "col.size", message: "Size" }), inUseBy: t({ id: "hostDetail.disk.inUseBy", message: "In use by" }) }[key]}</TableHeaderCell>
              ))}
            </TableRow>
          </TableHead>
          <TableBody>
            {(listAt(host, "status.inventory.disks") as Disk[]).map((disk) => (
              <TableRow key={disk.path}>
                <TableCell><Ltr>{disk.path}</Ltr></TableCell>
                <TableCell><Ltr>{disk.model ?? ""}</Ltr></TableCell>
                <TableCell><Ltr>{disk.serial ?? ""}</Ltr></TableCell>
                <TableCell>{formatBytes(disk.sizeBytes, locale)}</TableCell>
                <TableCell>{disk.inUseBy ?? "—"}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Section>
      <Section title={t({ id: "hostDetail.nics", message: "Network interfaces" })}>
        <Table caption={t({ id: "hostDetail.nics", message: "Network interfaces" })}>
          <TableHead>
            <TableRow>
              <TableHeaderCell>{t({ id: "col.name", message: "Name" })}</TableHeaderCell>
              <TableHeaderCell>MAC</TableHeaderCell>
              <TableHeaderCell>PCI</TableHeaderCell>
              <TableHeaderCell>{t({ id: "hostDetail.speed", message: "Speed" })}</TableHeaderCell>
              <TableHeaderCell>{t({ id: "hostDetail.link", message: "Link" })}</TableHeaderCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {(listAt(host, "status.inventory.nics") as Nic[]).map((nic) => (
              <TableRow key={nic.name}>
                <TableCell><Ltr>{nic.name}</Ltr></TableCell>
                <TableCell><Ltr>{nic.mac}</Ltr></TableCell>
                <TableCell><Ltr>{nic.pciAddress ?? ""}</Ltr></TableCell>
                <TableCell>{nic.speedMbps ? `${nic.speedMbps} Mbps` : "—"}</TableCell>
                <TableCell><Badge tone={nic.link ? "success" : "danger"}>{nic.link ? t({ id: "hostDetail.linkUp", message: "Up" }) : t({ id: "hostDetail.linkDown", message: "Down" })}</Badge></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Section>
      <Section title={t({ id: "hostDetail.pci", message: "PCI devices" })}>
        <Table caption={t({ id: "hostDetail.pci", message: "PCI devices" })}>
          <TableHead>
            <TableRow>
              <TableHeaderCell>{t({ id: "col.address", message: "Address" })}</TableHeaderCell>
              <TableHeaderCell>{t({ id: "hostDetail.vendor", message: "Vendor" })}</TableHeaderCell>
              <TableHeaderCell>{t({ id: "hostDetail.device", message: "Device" })}</TableHeaderCell>
              <TableHeaderCell>{t({ id: "hostDetail.class", message: "Class" })}</TableHeaderCell>
              <TableHeaderCell>{t({ id: "hostDetail.driver", message: "Driver" })}</TableHeaderCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {(listAt(host, "status.inventory.pci") as Pci[]).map((device) => (
              <TableRow key={device.address}>
                <TableCell><Ltr>{device.address}</Ltr></TableCell>
                <TableCell><Ltr>{device.vendor}</Ltr></TableCell>
                <TableCell><Ltr>{device.device}</Ltr></TableCell>
                <TableCell><Ltr>{device.class}</Ltr></TableCell>
                <TableCell><Ltr>{device.driver ?? ""}</Ltr></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Section>
      <Section title={t({ id: "hostDetail.firmware", message: "Firmware and system" })}>
        <dl className="br-facts">
          <dt>BIOS</dt>
          <dd><Ltr>{textAt(host, "status.inventory.firmware.bios") || "—"}</Ltr></dd>
          <dt>{t({ id: "hostDetail.kernel", message: "Kernel" })}</dt>
          <dd><Ltr>{textAt(host, "status.inventory.firmware.kernel") || "—"}</Ltr></dd>
          <dt>OS</dt>
          <dd><Ltr>{`${textAt(host, "status.inventory.firmware.os")} ${textAt(host, "status.inventory.firmware.osVersion")}`.trim() || "—"}</Ltr></dd>
        </dl>
      </Section>
    </>
  );
}

export default function HostDetailPage() {
  const { t } = useLingui();
  const { name = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const def = definitionFor("hosts");
  const http = useHttp();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const [editing, setEditing] = useState(false);
  const ref = def ? refOf(def) : { group: "", version: "", plural: "", namespaced: false };
  const query = useLiveObject(ref, undefined, name);
  const [canUpdate] = useCans([{ verb: "update", group: ref.group, resource: ref.plural, name }]);
  if (!def) {
    return null;
  }
  if (query.isPending) {
    return <Spinner />;
  }
  const host = query.data;
  if (!host) {
    return <EmptyState title={t({ id: "detail.deleted.title", message: "This object was deleted" })} />;
  }
  const maintenanceOn = host.spec?.maintenance === true;
  const pending = maintenanceOn !== (conditionOf(host, "MaintenanceMode")?.status === "True");

  async function setMaintenance(enabled: boolean) {
    try {
      await kubeClient(http).mergePatch(ref, undefined, name, { spec: { maintenance: enabled } });
      void queryClient.invalidateQueries({ queryKey: objectKey(ref, undefined, name) });
      void queryClient.invalidateQueries({ queryKey: listKey(ref, undefined, {}) });
    } catch (error) {
      toast.show({ tone: "danger", title: t({ id: "action.error.failed", message: "The action failed" }), description: error instanceof Error ? error.message : "" });
    }
  }

  const tab = params.get("tab") ?? "overview";
  return (
    <>
      <PageHeader
        title={name}
        breadcrumbs={[{ label: t({ id: "nav.item.hosts", message: "Hosts" }), to: "/admin/hosts" }, { label: name }]}
        actions={
          canUpdate === "allowed" ? (
            <>
              <Switch
                label={t({ id: "hostDetail.maintenance", message: "Maintenance mode" })}
                checked={maintenanceOn}
                disabled={pending}
                onCheckedChange={(next) => (next ? setConfirming(true) : void setMaintenance(false))}
              />
              <Button variant="secondary" onClick={() => setEditing(true)}>
                {t({ id: "hostDetail.editRoles", message: "Edit roles" })}
              </Button>
            </>
          ) : null
        }
      />
      <Tabs value={tab} onValueChange={(next) => setParams({ tab: next })}>
        <TabList>
          <Tab value="overview">{t({ id: "hostDetail.tab.overview", message: "Overview" })}</Tab>
          <Tab value="inventory">{t({ id: "hostDetail.tab.inventory", message: "Inventory" })}</Tab>
          <Tab value="metrics">{t({ id: "hostDetail.tab.metrics", message: "Metrics" })}</Tab>
        </TabList>
        <TabPanel value="overview"><Overview host={host} /></TabPanel>
        <TabPanel value="inventory"><Inventory host={host} /></TabPanel>
        <TabPanel value="metrics">
          <Suspense fallback={<Spinner />}>
            <HostMetrics />
          </Suspense>
        </TabPanel>
      </Tabs>
      <ConfirmSimpleDialog
        open={confirming}
        title={t({ id: "hostDetail.maintenance.title", message: "Enter maintenance mode" })}
        description={t({ id: "hostDetail.maintenance.description", message: "Virtual machines on this host migrate away. Ceph sets noout for its disks. The host stays out of scheduling until you turn the mode off." })}
        confirmLabel={t({ id: "hostDetail.maintenance.confirm", message: "Enter maintenance" })}
        onConfirm={() => void setMaintenance(true)}
        onCancel={() => setConfirming(false)}
      />
      {editing ? <LazyEditDialog def={def} object={host} onClose={() => setEditing(false)} /> : null}
    </>
  );
}
