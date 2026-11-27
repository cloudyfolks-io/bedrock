import { lazy, Suspense, useState, type ReactNode } from "react";
import { useParams, useSearchParams } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Badge, Button, Switch, Tab, TabList, TabPanel, TableBody, TableCell, TableHead, TableHeaderCell, TableRow, Tabs, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { conditionOf, kubeClient } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { ConfirmSimpleDialog } from "../../components/ConfirmDialogs";
import { EmptyState } from "../../components/EmptyState";
import { Ltr } from "../../components/Ltr";
import { PageHeader } from "../../components/PageHeader";
import { QueryFailure } from "../../components/QueryFailure";
import { Spinner } from "../../components/Spinner";
import { StatusBadge } from "../../components/StatusBadge";
import { LazyEditDialog } from "../../forms/LazyDialogs";
import { formatBytes, formatNumber } from "../../format";
import { useLocale } from "../../LocaleProvider";
import { listAt, textAt } from "../../resources/path";
import { definitionFor, refOf } from "../../resources/registry";
import { useLiveObject } from "../../stream/live";
import styles from "../operator.module.css";
import { hostSummary } from "./hostSummary";
import { OperatorSection as Section, OperatorTable } from "./OperatorSection";

const HostMetrics = lazy(() => import("../../charts/HostMetrics"));

const conditionTypes = ["Ready", "ManagementApplied", "MaintenanceMode", "RebootPending"];

interface Disk { path: string; model?: string; serial?: string; sizeBytes: number; inUseBy?: string }
interface Nic { name: string; mac: string; speedMbps?: number; link: boolean; pciAddress?: string }
interface Pci { address: string; vendor: string; device: string; class: string; driver?: string }
interface Step { name: string; state: string; message?: string }

function Overview({ host }: { host: KubeObject }) {
  const { t } = useLingui();
  const summary = hostSummary(host);
  const steps = listAt(host, "status.applied.steps") as Step[];
  return (
    <>
      <dl className={`${styles.surface} ${styles.facts}`}>
        <dt>{t({ id: "hostDetail.roles", message: "Roles" })}</dt>
        <dd>{summary.roles.length ? summary.roles.map((role) => <Badge key={role} tone="neutral"><Ltr>{role}</Ltr></Badge>) : "—"}</dd>
        <dt>{t({ id: "hostDetail.window", message: "Maintenance window" })}</dt>
        <dd>{textAt(host, "spec.maintenanceWindow") || "—"}</dd>
        <dt>{t({ id: "hostDetail.version", message: "Kubernetes version" })}</dt>
        <dd><Ltr>{textAt(host, "status.kubernetesVersion") || "—"}</Ltr></dd>
      </dl>
      <OperatorTable title={t({ id: "detail.conditions.title", message: "Conditions" })}>
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
      </OperatorTable>
      {steps.length ? (
        <OperatorTable title={t({ id: "hostDetail.applied", message: "Applied configuration" })}>
          <TableHead>
            <TableRow>
              <TableHeaderCell>{t({ id: "hostDetail.step", message: "Step" })}</TableHeaderCell>
              <TableHeaderCell>{t({ id: "detail.conditions.status", message: "Status" })}</TableHeaderCell>
              <TableHeaderCell>{t({ id: "detail.conditions.message", message: "Message" })}</TableHeaderCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {steps.map((step) => (
              <TableRow key={step.name}>
                <TableCell><Ltr>{step.name}</Ltr></TableCell>
                <TableCell>{step.state}</TableCell>
                <TableCell>{step.message ?? ""}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </OperatorTable>
      ) : (
        <Section title={t({ id: "hostDetail.applied", message: "Applied configuration" })}>
          <p className={`${styles.sectionBody} ${styles.help}`}>{t({ id: "hostDetail.appliedEmpty", message: "No configuration steps reported." })}</p>
        </Section>
      )}
    </>
  );
}

function HardwareList({ title, empty, rows }: { title: string; empty: string; rows: { id: string; fields: [string, ReactNode][] }[] }) {
  return (
    <Section title={title}>
      {rows.length ? (
        <ul className={styles.hardware} role="list">
          {rows.map(({ id, fields }) => (
            <li key={id}>
              <dl>
                {fields.map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value || "—"}</dd></div>)}
              </dl>
            </li>
          ))}
        </ul>
      ) : <p className={`${styles.sectionBody} ${styles.help}`}>{empty}</p>}
    </Section>
  );
}

function Inventory({ host }: { host: KubeObject }) {
  const { t } = useLingui();
  const { locale } = useLocale();
  const memory = hostSummary(host).memory;
  const model = textAt(host, "status.inventory.cpu.model");
  const cores = textAt(host, "status.inventory.cpu.cores");
  const threads = textAt(host, "status.inventory.cpu.threads");
  const sockets = textAt(host, "status.inventory.cpu.sockets");
  const bios = textAt(host, "status.inventory.firmware.bios");
  const kernel = textAt(host, "status.inventory.firmware.kernel");
  const os = `${textAt(host, "status.inventory.firmware.os")} ${textAt(host, "status.inventory.firmware.osVersion")}`.trim();
  return (
    <>
      <Section title={t({ id: "hostDetail.cpu", message: "CPU" })}>
        {model || cores || threads || sockets || memory ? (
          <dl className={styles.facts}>
            <dt>{t({ id: "hostDetail.cpuModel", message: "Model" })}</dt>
            <dd><Ltr>{model || "—"}</Ltr></dd>
            <dt>{t({ id: "hostDetail.cores", message: "Cores" })}</dt>
            <dd>{cores ? formatNumber(Number(cores), locale) : "—"}</dd>
            <dt>{t({ id: "hostDetail.threads", message: "Threads" })}</dt>
            <dd>{threads ? formatNumber(Number(threads), locale) : "—"}</dd>
            <dt>{t({ id: "hostDetail.sockets", message: "Sockets" })}</dt>
            <dd>{sockets ? formatNumber(Number(sockets), locale) : "—"}</dd>
            <dt>{t({ id: "hostDetail.memory", message: "Memory" })}</dt>
            <dd>{memory ? formatBytes(memory, locale) : "—"}</dd>
          </dl>
        ) : <p className={`${styles.sectionBody} ${styles.help}`}>{t({ id: "hostDetail.cpuEmpty", message: "No CPU or memory data reported." })}</p>}
      </Section>
      <HardwareList
        title={t({ id: "hostDetail.disks", message: "Disks" })}
        empty={t({ id: "hostDetail.disksEmpty", message: "No disks reported." })}
        rows={(listAt(host, "status.inventory.disks") as Disk[]).map((disk) => ({
          id: disk.path,
          fields: [
            [t({ id: "hostDetail.disk.path", message: "Path" }), <Ltr>{disk.path}</Ltr>],
            [t({ id: "hostDetail.cpuModel", message: "Model" }), disk.model && <Ltr>{disk.model}</Ltr>],
            [t({ id: "hostDetail.disk.serial", message: "Serial" }), disk.serial && <Ltr>{disk.serial}</Ltr>],
            [t({ id: "col.size", message: "Size" }), formatBytes(disk.sizeBytes, locale)],
            [t({ id: "hostDetail.disk.inUseBy", message: "In use by" }), disk.inUseBy && <Ltr>{disk.inUseBy}</Ltr>],
          ],
        }))}
      />
      <HardwareList
        title={t({ id: "hostDetail.nics", message: "Network interfaces" })}
        empty={t({ id: "hostDetail.nicsEmpty", message: "No network interfaces reported." })}
        rows={(listAt(host, "status.inventory.nics") as Nic[]).map((nic) => ({
          id: nic.name,
          fields: [
            [t({ id: "col.name", message: "Name" }), <Ltr>{nic.name}</Ltr>],
            ["MAC", <Ltr>{nic.mac}</Ltr>],
            ["PCI", nic.pciAddress && <Ltr>{nic.pciAddress}</Ltr>],
            [t({ id: "hostDetail.speed", message: "Speed" }), nic.speedMbps ? `${formatNumber(nic.speedMbps, locale)} Mbps` : undefined],
            [t({ id: "hostDetail.link", message: "Link" }), <Badge tone={nic.link ? "success" : "danger"}>{nic.link ? t({ id: "hostDetail.linkUp", message: "Up" }) : t({ id: "hostDetail.linkDown", message: "Down" })}</Badge>],
          ],
        }))}
      />
      <HardwareList
        title={t({ id: "hostDetail.pci", message: "PCI devices" })}
        empty={t({ id: "hostDetail.pciEmpty", message: "No PCI devices reported." })}
        rows={(listAt(host, "status.inventory.pci") as Pci[]).map((device) => ({
          id: device.address,
          fields: [
            [t({ id: "col.address", message: "Address" }), <Ltr>{device.address}</Ltr>],
            [t({ id: "hostDetail.vendor", message: "Vendor" }), <Ltr>{device.vendor}</Ltr>],
            [t({ id: "hostDetail.device", message: "Device" }), <Ltr>{device.device}</Ltr>],
            [t({ id: "hostDetail.class", message: "Class" }), <Ltr>{device.class}</Ltr>],
            [t({ id: "hostDetail.driver", message: "Driver" }), device.driver && <Ltr>{device.driver}</Ltr>],
          ],
        }))}
      />
      <Section title={t({ id: "hostDetail.firmware", message: "Firmware and system" })}>
        {bios || kernel || os ? (
          <dl className={styles.facts}>
            <dt>BIOS</dt>
            <dd><Ltr>{bios || "—"}</Ltr></dd>
            <dt>{t({ id: "hostDetail.kernel", message: "Kernel" })}</dt>
            <dd><Ltr>{kernel || "—"}</Ltr></dd>
            <dt>OS</dt>
            <dd><Ltr>{os || "—"}</Ltr></dd>
          </dl>
        ) : <p className={`${styles.sectionBody} ${styles.help}`}>{t({ id: "hostDetail.firmwareEmpty", message: "No firmware or system data reported." })}</p>}
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
  const [confirming, setConfirming] = useState(false);
  const [editing, setEditing] = useState(false);
  const ref = def ? refOf(def) : { group: "", version: "", plural: "", namespaced: false };
  const query = useLiveObject(ref, undefined, name);
  const [canPatch, canUpdate] = useCans([
    { verb: "patch", group: ref.group, resource: ref.plural, name },
    { verb: "update", group: ref.group, resource: ref.plural, name },
  ]);
  if (!def) {
    return null;
  }
  if (query.isPending) {
    return <Spinner />;
  }
  if (query.isError) {
    return <><PageHeader title={name} /><QueryFailure error={query.error} retry={() => void query.refetch()} /></>;
  }
  const host = query.data;
  if (!host) {
    return <EmptyState title={t({ id: "detail.deleted.title", message: "This object was deleted" })} />;
  }
  const maintenanceOn = host.spec?.maintenance === true;
  const pending = maintenanceOn !== (conditionOf(host, "MaintenanceMode")?.status === "True");
  const ready = conditionOf(host, "Ready");

  async function setMaintenance(enabled: boolean) {
    try {
      await kubeClient(http).mergePatch(ref, undefined, name, { spec: { maintenance: enabled } });
    } catch (error) {
      toast.show({ tone: "danger", title: t({ id: "action.error.failed", message: "The action failed" }), description: error instanceof Error ? error.message : "" });
    }
  }

  const requestedTab = params.get("tab");
  const tab = requestedTab === "inventory" || requestedTab === "metrics" ? requestedTab : "overview";
  return (
    <div className={`${styles.stack} ${styles.host}`}>
      <PageHeader
        title={name}
        breadcrumbs={[{ label: t({ id: "nav.item.hosts", message: "Hosts" }), to: "/admin/hosts" }, { label: name }]}
        actions={(
          <>
            <div className={styles.health} role="status">
              <StatusBadge obj={host} />
              {ready?.reason && ready.reason !== "Ready" ? <Ltr>{ready.reason}</Ltr> : null}
              {ready?.message ? <bdi>{ready.message}</bdi> : null}
            </div>
            <div className={`${styles.actions} ${styles.hostControls}`}>
              {canPatch === "allowed" ? (
                <Switch
                  label={t({ id: "hostDetail.maintenance", message: "Maintenance mode" })}
                  checked={maintenanceOn}
                  disabled={pending}
                  onCheckedChange={(next) => (next ? setConfirming(true) : void setMaintenance(false))}
                />
              ) : null}
              {canUpdate === "allowed" ? (
                <Button variant="secondary" onClick={() => setEditing(true)}>
                  {t({ id: "hostDetail.editRoles", message: "Edit roles" })}
                </Button>
              ) : null}
            </div>
          </>
        )}
      />
      <Tabs value={tab} onValueChange={(next) => {
        const updated = new URLSearchParams(params);
        updated.set("tab", next);
        setParams(updated);
      }}>
        <TabList>
          <Tab value="overview">{t({ id: "hostDetail.tab.overview", message: "Overview" })}</Tab>
          <Tab value="inventory">{t({ id: "hostDetail.tab.inventory", message: "Inventory" })}</Tab>
          <Tab value="metrics">{t({ id: "hostDetail.tab.metrics", message: "Metrics" })}</Tab>
        </TabList>
        <TabPanel value="overview"><div className={styles.stack}><Overview host={host} /></div></TabPanel>
        <TabPanel value="inventory"><div className={styles.stack}><Inventory host={host} /></div></TabPanel>
        <TabPanel value="metrics">
          <Section title={t({ id: "hostDetail.tab.metrics", message: "Metrics" })}>
            <div className={styles.sectionBody}>
              <Suspense fallback={<Spinner />}>
                <HostMetrics />
              </Suspense>
            </div>
          </Section>
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
    </div>
  );
}
