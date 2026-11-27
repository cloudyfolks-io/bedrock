import { useState } from "react";
import { Link, useParams } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { msg } from "@lingui/core/macro";
import { Button, Progress, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow } from "@bedrock/design";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { EmptyState } from "../../components/EmptyState";
import { Ltr } from "../../components/Ltr";
import { PageHeader } from "../../components/PageHeader";
import { QueryFailure } from "../../components/QueryFailure";
import { Spinner } from "../../components/Spinner";
import { LazyCreateDialog, LazyEditDialog } from "../../forms/LazyDialogs";
import { formatBytes, formatNumber, parseQuantity } from "../../format";
import { useLocale } from "../../LocaleProvider";
import { listAt, textAt } from "../../resources/path";
import { definitionFor, refOf, type ResourceDef } from "../../resources/registry";
import { useActionRunner } from "../../resources/useActionRunner";
import { useLiveList, useLiveObject } from "../../stream/live";
import styles from "../operator.module.css";
import { OperatorSection } from "./OperatorSection";

const quotaLabels = {
  cpu: msg({ id: "wizard.cores", message: "CPU cores" }),
  memory: msg({ id: "col.memory", message: "Memory" }),
  storage: msg({ id: "tenantDetail.quota.storage", message: "Storage" }),
  projects: msg({ id: "tenantDetail.projects", message: "Projects" }),
  elasticIPs: msg({ id: "nav.item.elasticIps", message: "Elastic IPs" }),
};
const quotaKeys = Object.keys(quotaLabels) as (keyof typeof quotaLabels)[];
const quotaBytes = msg({
  id: "tenantDetail.quota.bytes",
  message: "{number} {unit, select, KiB {KiB} MiB {MiB} GiB {GiB} TiB {TiB} PiB {PiB} other {B}}",
});

function required(plural: string): ResourceDef {
  const def = definitionFor(plural);
  if (!def) {
    throw new Error(`missing definition ${plural}`);
  }
  return def;
}

function amount(value: unknown): number {
  return typeof value === "number" ? value : typeof value === "string" ? parseQuantity(value) : Number.NaN;
}

function QuotaRows({ tenant }: { tenant: KubeObject }) {
  const { t, i18n } = useLingui();
  const { locale } = useLocale();
  const quota = (tenant.spec?.quota ?? {}) as Record<string, unknown>;
  const usage = (tenant.status?.usage ?? {}) as Record<string, unknown>;
  const keys = quotaKeys.filter((key) => quota[key] !== undefined);
  if (keys.length === 0) {
    return <p className={`${styles.sectionBody} ${styles.help}`}>{t({ id: "tenantDetail.noQuota", message: "No quota is set." })}</p>;
  }
  function formatAmount(key: keyof typeof quotaLabels, value: number, raw: unknown): string {
    if (!Number.isFinite(value)) return typeof raw === "string" ? raw : "—";
    if (key !== "memory" && key !== "storage") return formatNumber(value, locale);
    const [number = "", unit = "B"] = formatBytes(value, locale).split(" ");
    return i18n._({ ...quotaBytes, values: { number, unit } });
  }
  return (
    <ul className={styles.quota} role="list">
      {keys.map((key) => {
        const used = amount(usage[key]);
        const max = amount(quota[key]);
        const resource = i18n._(quotaLabels[key]);
        const limit = formatAmount(key, max, quota[key]);
        const usedDisplay = formatAmount(key, used, usage[key]);
        const label = usage[key] === undefined
          ? t({ id: "tenantDetail.quota.noUsage", message: `${resource}: ${limit} limit (usage not reported)` })
          : t({ id: "tenantDetail.quota.usage", message: `${resource}: ${usedDisplay} / ${limit}` });
        return (
          <li key={key} className={styles.sectionBody}>
            {Number.isFinite(used) && Number.isFinite(max) && max > 0
              ? <Progress label={label} value={Math.min(used, max)} max={max} />
              : label}
          </li>
        );
      })}
    </ul>
  );
}

export default function TenantDetailPage() {
  const { t } = useLingui();
  const { name = "" } = useParams();
  const tenantDef = required("tenants");
  const projectDef = required("projects");
  const tenant = useLiveObject(refOf(tenantDef), undefined, name);
  const projects = useLiveList(refOf(projectDef), {});
  const runner = useActionRunner(projectDef);
  const [creating, setCreating] = useState(false);
  const [editingProject, setEditingProject] = useState<KubeObject | undefined>();
  const [editingTenant, setEditingTenant] = useState(false);
  const [canCreate, canUpdate, canDelete] = useCans([
    { verb: "create", group: projectDef.group, resource: "projects" },
    { verb: "update", group: projectDef.group, resource: "projects" },
    { verb: "delete", group: projectDef.group, resource: "projects" },
  ]);
  const [canEditTenant] = useCans([{ verb: "update", group: tenantDef.group, resource: "tenants", name }]);
  if (tenant.isPending) {
    return <Spinner />;
  }
  if (tenant.isError) {
    return <><PageHeader title={name} /><QueryFailure error={tenant.error} retry={() => void tenant.refetch()} /></>;
  }
  if (!tenant.data) {
    return <EmptyState title={t({ id: "detail.deleted.title", message: "This object was deleted" })} />;
  }
  const own = (projects.data?.items ?? []).filter((project) => textAt(project, "spec.tenantRef") === name);
  const deleteAction = projectDef.actions.find((action) => action.id === "delete");

  return (
    <div className={styles.stack}>
      <PageHeader
        title={textAt(tenant.data, "spec.displayName") || name}
        breadcrumbs={[{ label: t({ id: "nav.item.tenants", message: "Tenants" }), to: "/admin/tenants" }, { label: name }]}
        actions={
          canEditTenant === "allowed" ? (
            <Button variant="secondary" onClick={() => setEditingTenant(true)}>
              {t({ id: "common.action.edit", message: "Edit" })}
            </Button>
          ) : null
        }
      />
      <dl className={`${styles.surface} ${styles.facts}`}>
        <dt>{t({ id: "col.vpcCidr", message: "VPC CIDR" })}</dt>
        <dd><Ltr>{textAt(tenant.data, "spec.network.vpcCIDR") || "—"}</Ltr></dd>
        <dt>{t({ id: "col.owners", message: "Owners" })}</dt>
        <dd>{(listAt(tenant.data, "spec.owners") as { kind: string; name: string }[]).map((owner) => `${owner.kind} ${owner.name}`).join(", ") || "—"}</dd>
      </dl>
      <OperatorSection title={t({ id: "tenantDetail.quota", message: "Quota use" })}>
        <QuotaRows tenant={tenant.data} />
      </OperatorSection>
      <OperatorSection title={t({ id: "tenantDetail.projects", message: "Projects" })} actions={
        canCreate === "allowed" && projects.isSuccess ? (
          <Button variant="primary" onClick={() => setCreating(true)}>
            {t({ id: "tenantDetail.createProject", message: "Create project" })}
          </Button>
        ) : null
      }>
        {projects.isPending ? <Spinner /> : null}
        {projects.isError ? <QueryFailure error={projects.error} retry={() => void projects.refetch()} /> : null}
        {projects.isSuccess && own.length === 0 ? (
          <p className={`${styles.sectionBody} ${styles.help}`}>{t({ id: "tenantDetail.projectsEmpty", message: "No projects in this tenant." })}</p>
        ) : null}
        {projects.isSuccess && own.length > 0 ? <Table caption={t({ id: "tenantDetail.projects", message: "Projects" })}>
          <TableHead>
            <TableRow>
              <TableHeaderCell>{t({ id: "col.name", message: "Name" })}</TableHeaderCell>
              <TableHeaderCell>{t({ id: "col.namespace", message: "Namespace" })}</TableHeaderCell>
              <TableHeaderCell>{t({ id: "col.members", message: "Members" })}</TableHeaderCell>
              <TableHeaderCell><span className="br-visually-hidden">{t({ id: "list.actions.header", message: "Actions" })}</span></TableHeaderCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {own.map((project) => (
              <TableRow key={project.metadata.uid ?? project.metadata.name}>
                <TableCell>{textAt(project, "status.namespace") ? <Link to={`/admin/inventory?project=${project.metadata.name}`}>{project.metadata.name}</Link> : project.metadata.name}</TableCell>
                <TableCell><Ltr>{textAt(project, "status.namespace")}</Ltr></TableCell>
                <TableCell>{String(listAt(project, "spec.members").length)}</TableCell>
                <TableCell>
                  <div className={styles.actions}>
                    {canUpdate === "allowed" ? (
                      <Button variant="secondary" onClick={() => setEditingProject(project)}>
                        {t({ id: "tenantDetail.editProject", message: `Edit ${project.metadata.name}` })}
                      </Button>
                    ) : null}
                    {canDelete === "allowed" && deleteAction ? (
                      <Button variant="danger" onClick={() => runner.request(deleteAction, project)}>
                        {t({ id: "tenantDetail.deleteProject", message: `Delete ${project.metadata.name}` })}
                      </Button>
                    ) : null}
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table> : null}
      </OperatorSection>
      {runner.dialogs}
      {creating ? <LazyCreateDialog def={projectDef} initialSpec={{ tenantRef: name }} lockedPaths={["tenantRef"]} onClose={() => setCreating(false)} /> : null}
      {editingProject ? <LazyEditDialog def={projectDef} object={editingProject} onClose={() => setEditingProject(undefined)} /> : null}
      {editingTenant ? <LazyEditDialog def={tenantDef} object={tenant.data} onClose={() => setEditingTenant(false)} /> : null}
    </div>
  );
}
