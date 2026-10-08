import { lazy, Suspense } from "react";
import { useParams, useSearchParams } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Badge, Tab, TabList, TabPanel, Tabs } from "@bedrock/design";
import type { Condition, KubeObject } from "../../api/types";
import { EmptyState } from "../../components/EmptyState";
import { Ltr } from "../../components/Ltr";
import { PageHeader } from "../../components/PageHeader";
import { Spinner } from "../../components/Spinner";
import { StatusBadge } from "../../components/StatusBadge";
import { listAt, textAt } from "../../resources/path";
import { definitionFor, refOf } from "../../resources/registry";
import { useCurrentProject } from "../../shell/context";
import { useLiveObject } from "../../stream/live";
import { VmActions, useVmAccess } from "./VmActions";
import { VmDisksTab } from "./VmDisksTab";
import { VmNetworksTab } from "./VmNetworksTab";

const ConsoleTab = lazy(() => import("../../console/ConsoleTab"));
const VmMetrics = lazy(() => import("../../charts/VmMetrics"));

interface Address { ip: string }

function Overview({ vm }: { vm: KubeObject }) {
  const { t } = useLingui();
  const conditions = (Array.isArray(vm.status?.conditions) ? vm.status.conditions : []) as Condition[];
  return (
    <>
      <dl className="br-facts">
        <dt>{t({ id: "col.instanceType", message: "Instance type" })}</dt>
        <dd><Ltr>{textAt(vm, "spec.instanceType")}</Ltr></dd>
        <dt>{t({ id: "vmDetail.preference", message: "Preference" })}</dt>
        <dd><Ltr>{textAt(vm, "spec.preference") || "—"}</Ltr></dd>
        <dt>{t({ id: "col.image", message: "Image" })}</dt>
        <dd><Ltr>{textAt(vm, "spec.image.name") || "—"}</Ltr> {textAt(vm, "spec.image.scope") ? <Badge tone="neutral">{textAt(vm, "spec.image.scope")}</Badge> : null}</dd>
        <dt>{t({ id: "vmDetail.firmware", message: "Firmware" })}</dt>
        <dd>{textAt(vm, "spec.firmware.uefi") === "true" ? "UEFI" : "BIOS"}</dd>
        <dt>{t({ id: "col.node", message: "Node" })}</dt>
        <dd>{textAt(vm, "status.node") || "—"}</dd>
        <dt>{t({ id: "col.addresses", message: "Addresses" })}</dt>
        <dd>{(listAt(vm, "status.addresses") as Address[]).map((entry) => <Ltr key={entry.ip}>{`${entry.ip} `}</Ltr>)}</dd>
        <dt>{t({ id: "vmDetail.guestAgent", message: "Guest agent" })}</dt>
        <dd><Ltr>{textAt(vm, "status.guestAgent.version") || "—"}</Ltr></dd>
        <dt>{t({ id: "col.os", message: "OS" })}</dt>
        <dd>{textAt(vm, "status.guestAgent.os") || "—"}</dd>
        <dt>{t({ id: "vmDetail.hostname", message: "Hostname" })}</dt>
        <dd><Ltr>{textAt(vm, "status.guestAgent.hostname") || "—"}</Ltr></dd>
        <dt>{t({ id: "col.elasticIp", message: "Elastic IP" })}</dt>
        <dd>{textAt(vm, "status.elasticIP") || "—"}</dd>
      </dl>
      <ul className="br-conditions">
        {conditions.map((condition) => (
          <li key={condition.type}>
            <Ltr>{condition.type}</Ltr>: {condition.status} {condition.message ?? ""}
          </li>
        ))}
      </ul>
    </>
  );
}

export default function VmDetailPage() {
  const { t, i18n } = useLingui();
  const { name = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const def = definitionFor("virtualmachines");
  const project = useCurrentProject();
  const namespace = project?.namespace;
  const access = useVmAccess(namespace);
  const ref = def ? refOf(def) : { group: "", version: "", plural: "", namespaced: true };
  const query = useLiveObject(ref, namespace, name);
  if (!def || !project) {
    return <Spinner />;
  }
  if (query.isPending) {
    return <Spinner />;
  }
  const vm = query.data;
  if (!vm) {
    return (
      <>
        <PageHeader title={name} />
        <EmptyState title={t({ id: "detail.deleted.title", message: "This object was deleted" })} />
      </>
    );
  }
  const tab = params.get("tab") ?? "overview";
  const allowVnc = access.vnc === "allowed";
  const allowSerial = access.serial === "allowed";
  const consoleAllowed = allowVnc || allowSerial;
  return (
    <>
      <PageHeader
        title={name}
        breadcrumbs={[{ label: i18n._(def.title), to: `/p/${project.name}/vms` }, { label: name }]}
        actions={<VmActions vm={vm} access={access.actions} />}
      />
      <p>
        <StatusBadge obj={vm} />
      </p>
      <Tabs value={tab} onValueChange={(next) => setParams({ tab: next })}>
        <TabList>
          <Tab value="overview">{t({ id: "vmDetail.tab.overview", message: "Overview" })}</Tab>
          {consoleAllowed ? <Tab value="console">{t({ id: "vmDetail.tab.console", message: "Console" })}</Tab> : null}
          <Tab value="metrics">{t({ id: "vmDetail.tab.metrics", message: "Metrics" })}</Tab>
          <Tab value="disks">{t({ id: "vmDetail.tab.disks", message: "Disks" })}</Tab>
          <Tab value="networks">{t({ id: "vmDetail.tab.networks", message: "Networks" })}</Tab>
        </TabList>
        <TabPanel value="overview"><Overview vm={vm} /></TabPanel>
        <TabPanel value="console">
          <Suspense fallback={<Spinner />}>
            <ConsoleTab vm={vm} allowVnc={allowVnc} allowSerial={allowSerial} />
          </Suspense>
        </TabPanel>
        <TabPanel value="metrics">
          <Suspense fallback={<Spinner />}>
            <VmMetrics vm={vm} />
          </Suspense>
        </TabPanel>
        <TabPanel value="disks"><VmDisksTab vm={vm} project={project.name} /></TabPanel>
        <TabPanel value="networks"><VmNetworksTab vm={vm} /></TabPanel>
      </Tabs>
    </>
  );
}
