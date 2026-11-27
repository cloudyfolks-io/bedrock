import { lazy, Suspense } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Tabs as BaseTabs } from "@base-ui/react/tabs";
import { Badge, Tab, TabPanel, Tabs } from "@bedrock/design";
import { findPendingWrite } from "../../api/pendingWrites";
import type { Condition, KubeObject } from "../../api/types";
import { EmptyState } from "../../components/EmptyState";
import { Ltr } from "../../components/Ltr";
import { PageHeader } from "../../components/PageHeader";
import { PendingWriteTag, useCurrentPendingWrites } from "../../components/PendingWrites";
import { QueryFailure } from "../../components/QueryFailure";
import { Spinner } from "../../components/Spinner";
import { StatusBadge } from "../../components/StatusBadge";
import { listAt, textAt } from "../../resources/path";
import { vmSize } from "../../resources/vmSize";
import { definitionFor, refOf } from "../../resources/registry";
import { useCurrentProject } from "../../shell/context";
import { useLiveObject } from "../../stream/live";
import { VmActions, useVmAccess } from "./VmActions";
import { VmDisksTab } from "./VmDisksTab";
import { VmNetworksTab } from "./VmNetworksTab";
import styles from "./project.module.css";

const ConsoleTab = lazy(() => import("../../console/ConsoleTab"));
const VmMetrics = lazy(() => import("../../charts/VmMetrics"));

interface Address { ip: string; subnet: string }

function Overview({ vm }: { vm: KubeObject }) {
  const { t } = useLingui();
  const conditions = (Array.isArray(vm.status?.conditions) ? vm.status.conditions : []) as Condition[];
  const addresses = listAt(vm, "status.addresses") as Address[];
  const image = textAt(vm, "spec.image.name");
  const imageScope = textAt(vm, "spec.image.scope");
  const rootVolume = textAt(vm, "spec.rootDisk.volumeRef");
  const size = textAt(vm, "spec.rootDisk.sizeGi");
  return (
    <div className={styles.stack}>
      <div className={styles.summary}>
        <section className={styles.summaryPanel} aria-labelledby="vm-connection">
          <div className={styles.sectionHeading}>
            <h2 id="vm-connection">{t({ id: "vmDetail.overview.connection", message: "Connection" })}</h2>
            <Link to="?tab=networks">{t({ id: "vmDetail.tab.networks", message: "Networks" })}</Link>
          </div>
          <dl className={styles.facts}>
            <div>
              <dt>{t({ id: "col.addresses", message: "Addresses" })}</dt>
              <dd className={styles.values}>{addresses.length ? addresses.map((entry) => <span className={styles.address} key={`${entry.subnet}/${entry.ip}`}><Ltr>{entry.ip}</Ltr></span>) : "—"}</dd>
            </div>
            <div>
              <dt>{t({ id: "col.elasticIp", message: "Elastic IP" })}</dt>
              <dd><Ltr>{textAt(vm, "status.elasticIP") || "—"}</Ltr></dd>
            </div>
            <div>
              <dt>{t({ id: "vmDetail.hostname", message: "Hostname" })}</dt>
              <dd><Ltr>{textAt(vm, "status.guestAgent.hostname") || "—"}</Ltr></dd>
            </div>
            <div>
              <dt>{t({ id: "col.os", message: "OS" })}</dt>
              <dd>{textAt(vm, "status.guestAgent.os") || "—"}</dd>
            </div>
            <div>
              <dt>{t({ id: "vmDetail.guestAgent", message: "Guest agent" })}</dt>
              <dd><Ltr>{textAt(vm, "status.guestAgent.version") || "—"}</Ltr></dd>
            </div>
          </dl>
        </section>
        <section className={styles.summaryPanel} aria-labelledby="vm-hardware">
          <div className={styles.sectionHeading}>
            <h2 id="vm-hardware">{t({ id: "vmDetail.overview.hardware", message: "Hardware" })}</h2>
            <Link to="?tab=disks">{t({ id: "vmDetail.tab.disks", message: "Disks" })}</Link>
          </div>
          <dl className={styles.facts}>
            <div>
              <dt>{t({ id: "col.instanceType", message: "Instance type" })}</dt>
              <dd><Ltr>{vmSize(vm) || "—"}</Ltr></dd>
            </div>
            <div>
              <dt>{t({ id: "vmDetail.rootDisk", message: "Root disk" })}</dt>
              <dd><Ltr>{rootVolume || (size ? `${size} GiB` : "—")}</Ltr></dd>
            </div>
            {image ? (
              <div>
                <dt>{t({ id: "col.image", message: "Image" })}</dt>
                <dd className={styles.values}><Ltr>{image}</Ltr>{imageScope ? <Badge tone="neutral">{imageScope}</Badge> : null}</dd>
              </div>
            ) : null}
            <div>
              <dt>{t({ id: "col.node", message: "Node" })}</dt>
              <dd><Ltr>{textAt(vm, "status.node") || "—"}</Ltr></dd>
            </div>
            <div>
              <dt>{t({ id: "vmDetail.firmware", message: "Firmware" })}</dt>
              <dd><Ltr>{textAt(vm, "spec.firmware.uefi") === "true" ? "UEFI" : "BIOS"}</Ltr></dd>
            </div>
            <div>
              <dt>{t({ id: "vmDetail.preference", message: "Preference" })}</dt>
              <dd><Ltr>{textAt(vm, "spec.preference") || "—"}</Ltr></dd>
            </div>
          </dl>
        </section>
      </div>
      {conditions.length > 0 ? (
        <section className={styles.conditions}>
          <h2>{t({ id: "detail.conditions.title", message: "Conditions" })}</h2>
          <ul role="list">
            {conditions.map((condition) => (
              <li key={condition.type}>
                <Ltr>{condition.type}</Ltr>: {condition.status}
                {condition.reason ? <> · <Ltr>{condition.reason}</Ltr></> : null}
                {condition.message ? <> · {condition.message}</> : null}
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </div>
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
  const pendingWrites = useCurrentPendingWrites();
  if (!def || !project) {
    return <Spinner />;
  }
  if (query.isPending) {
    return <Spinner />;
  }
  const breadcrumbs = [{ label: i18n._(def.title), to: `/p/${project.name}/vms` }, { label: name }];
  if (query.isError) {
    return (
      <>
        <PageHeader title={name} breadcrumbs={breadcrumbs} />
        <QueryFailure error={query.error} retry={() => void query.refetch()} />
      </>
    );
  }
  const vm = query.data;
  if (!vm) {
    return (
      <>
        <PageHeader title={name} breadcrumbs={breadcrumbs} />
        <EmptyState title={t({ id: "detail.deleted.title", message: "This object was deleted" })} />
      </>
    );
  }
  const allowVnc = access.vnc === "allowed";
  const allowSerial = access.serial === "allowed";
  const consoleAllowed = allowVnc || allowSerial;
  const requestedTab = params.get("tab");
  const tab = (requestedTab === "console" && consoleAllowed) || requestedTab === "metrics" || requestedTab === "disks" || requestedTab === "networks"
    ? requestedTab : "overview";
  const pending = vm.metadata.deletionTimestamp ? "delete" : findPendingWrite(pendingWrites, ref, vm)?.kind;
  return (
    <div className={`${styles.stack} ${styles.tabs} ${styles.vmView}`}>
      <PageHeader
        title={name}
        breadcrumbs={breadcrumbs}
        actions={
          <>
            <div className={styles.status} role="status">
              <span className="br-visually-hidden">{t({ id: "col.status", message: "Status" })}: </span>
              <StatusBadge obj={vm} />
              {pending ? <PendingWriteTag kind={pending} /> : null}
            </div>
            <VmActions vm={vm} access={access.actions} pending={Boolean(pending)} />
          </>
        }
      />
      <Tabs value={tab} onValueChange={(next) => setParams({ tab: next })}>
        <BaseTabs.List className={`br-tab-list ${styles.vmTabList}`} aria-label={i18n._(def.title)}>
          <Tab value="overview">{t({ id: "vmDetail.tab.overview", message: "Overview" })}</Tab>
          {consoleAllowed ? <Tab value="console">{t({ id: "vmDetail.tab.console", message: "Console" })}</Tab> : null}
          <Tab value="metrics">{t({ id: "vmDetail.tab.metrics", message: "Metrics" })}</Tab>
          <Tab value="disks">{t({ id: "vmDetail.tab.disks", message: "Disks" })}</Tab>
          <Tab value="networks">{t({ id: "vmDetail.tab.networks", message: "Networks" })}</Tab>
        </BaseTabs.List>
        <TabPanel value="overview"><Overview vm={vm} /></TabPanel>
        {consoleAllowed ? (
          <TabPanel value="console">
            <div className={styles.console}>
              <Suspense fallback={<Spinner />}>
                <ConsoleTab vm={vm} allowVnc={allowVnc} allowSerial={allowSerial} />
              </Suspense>
            </div>
          </TabPanel>
        ) : null}
        <TabPanel value="metrics">
          <div className={styles.metrics}>
            <Suspense fallback={<Spinner />}>
              <VmMetrics vm={vm} />
            </Suspense>
          </div>
        </TabPanel>
        <TabPanel value="disks"><VmDisksTab vm={vm} project={project.name} /></TabPanel>
        <TabPanel value="networks"><VmNetworksTab vm={vm} /></TabPanel>
      </Tabs>
    </div>
  );
}
