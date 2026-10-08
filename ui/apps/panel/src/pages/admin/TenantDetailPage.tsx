import { useState } from "react";
import { useParams } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Button, Progress, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow } from "@bedrock/design";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { EmptyState } from "../../components/EmptyState";
import { Ltr } from "../../components/Ltr";
import { PageHeader } from "../../components/PageHeader";
import { Spinner } from "../../components/Spinner";
import { LazyCreateDialog, LazyEditDialog } from "../../forms/LazyDialogs";
import { formatQuantity, parseQuantity } from "../../format";
import { listAt, textAt } from "../../resources/path";
import { definitionFor, refOf, type ResourceDef } from "../../resources/registry";
import { useActionRunner } from "../../resources/useActionRunner";
import { useLiveList, useLiveObject } from "../../stream/live";

const quotaKeys = ["cpu", "memory", "storage", "projects", "elasticIPs"];

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
  const { t } = useLingui();
  const quota = (tenant.spec?.quota ?? {}) as Record<string, unknown>;
  const usage = (tenant.status?.usage ?? {}) as Record<string, unknown>;
  const keys = quotaKeys.filter((key) => quota[key] !== undefined);
  if (keys.length === 0) {
    return <p>{t({ id: "tenantDetail.noQuota", message: "No quota is set." })}</p>;
  }
  return (
    <ul className="br-quota">
      {keys.map((key) => {
        const used = amount(usage[key] ?? 0);
        const max = amount(quota[key]);
        return (
          <li key={key}>
            <Progress label={`${key}: ${String(usage[key] ?? 0)} / ${String(quota[key])}`} value={Number.isFinite(used) ? used : 0} max={Number.isFinite(max) && max > 0 ? max : 1} />
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
  if (!tenant.data) {
    return <EmptyState title={t({ id: "detail.deleted.title", message: "This object was deleted" })} />;
  }
  const own = (projects.data?.items ?? []).filter((project) => textAt(project, "spec.tenantRef") === name);
  const deleteAction = projectDef.actions.find((action) => action.id === "delete");

  return (
    <>
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
      <dl className="br-facts">
        <dt>{t({ id: "col.vpcCidr", message: "VPC CIDR" })}</dt>
        <dd><Ltr>{textAt(tenant.data, "spec.network.vpcCIDR")}</Ltr></dd>
        <dt>{t({ id: "col.owners", message: "Owners" })}</dt>
        <dd>{(listAt(tenant.data, "spec.owners") as { kind: string; name: string }[]).map((owner) => `${owner.kind} ${owner.name}`).join(", ") || "—"}</dd>
        <dt>{t({ id: "tenantDetail.memoryQuota", message: "Memory quota" })}</dt>
        <dd>{textAt(tenant.data, "spec.quota.memory") ? formatQuantity(textAt(tenant.data, "spec.quota.memory")) : "—"}</dd>
      </dl>
      <section className="br-section">
        <h2>{t({ id: "tenantDetail.quota", message: "Quota use" })}</h2>
        <QuotaRows tenant={tenant.data} />
      </section>
      <section className="br-section">
        <h2>{t({ id: "tenantDetail.projects", message: "Projects" })}</h2>
        {canCreate === "allowed" ? (
          <Button variant="primary" onClick={() => setCreating(true)}>
            {t({ id: "tenantDetail.createProject", message: "Create project" })}
          </Button>
        ) : null}
        <Table caption={t({ id: "tenantDetail.projects", message: "Projects" })}>
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
                <TableCell>{project.metadata.name}</TableCell>
                <TableCell><Ltr>{textAt(project, "status.namespace")}</Ltr></TableCell>
                <TableCell>{String(listAt(project, "spec.members").length)}</TableCell>
                <TableCell>
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
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </section>
      {runner.dialogs}
      {creating ? <LazyCreateDialog def={projectDef} initialSpec={{ tenantRef: name }} lockedPaths={["tenantRef"]} onClose={() => setCreating(false)} /> : null}
      {editingProject ? <LazyEditDialog def={projectDef} object={editingProject} onClose={() => setEditingProject(undefined)} /> : null}
      {editingTenant ? <LazyEditDialog def={tenantDef} object={tenant.data} onClose={() => setEditingTenant(false)} /> : null}
    </>
  );
}
