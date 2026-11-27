import { useId, useMemo, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Button, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow, useToast } from "@bedrock/design";
import { findPendingWrite } from "../api/pendingWrites";
import type { Condition, KubeObject } from "../api/types";
import { useProjects } from "../api/projects";
import { useCans, type Check } from "../authz/authz";
import { EmptyState } from "../components/EmptyState";
import { Ltr } from "../components/Ltr";
import { PageHeader } from "../components/PageHeader";
import { PendingWriteTag, useCurrentPendingWrites } from "../components/PendingWrites";
import { QueryFailure } from "../components/QueryFailure";
import { Spinner } from "../components/Spinner";
import { StatusBadge } from "../components/StatusBadge";
import { NotFoundPage } from "../pages/NotFoundPage";
import { LazyEditDialog } from "../forms/LazyDialogs";
import type { Locale } from "../i18n";
import { useCurrentProjectName } from "../shell/context";
import { useLiveObject } from "../stream/live";
import { detailFacts, specificationJSON } from "./detailFacts";
import { refOf, type ResourceDef } from "./registry";
import { useActionRunner } from "./useActionRunner";
import styles from "./resourceDetail.module.css";

function ConditionsTable({ conditions }: { conditions: Condition[] }) {
  const { t } = useLingui();
  const title = t({ id: "detail.conditions.title", message: "Conditions" });
  return (
    <section className={styles.panel}>
      <h2 className={styles.sectionHeading}>{title}</h2>
      <Table caption={title}>
        <TableHead>
          <TableRow>
            <TableHeaderCell>{t({ id: "detail.conditions.type", message: "Type" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "detail.conditions.status", message: "Status" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "detail.conditions.reason", message: "Reason" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "detail.conditions.message", message: "Message" })}</TableHeaderCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {conditions.map((condition) => (
            <TableRow key={condition.type}>
              <TableCell>
                <Ltr>{condition.type}</Ltr>
              </TableCell>
              <TableCell>{condition.status}</TableCell>
              <TableCell>{condition.reason ? <Ltr>{condition.reason}</Ltr> : "—"}</TableCell>
              <TableCell>{condition.message ?? ""}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </section>
  );
}

function DetailFacts({ def, object, projectName }: { def: ResourceDef; object: KubeObject; projectName?: string | undefined }) {
  const { i18n, t } = useLingui();
  const toast = useToast();
  const id = useId();
  const facts = detailFacts(def.plural, object, i18n.locale as Locale).filter(({ values }) => values.length > 0);
  const copyLabel = t({ id: "detail.copy.label", message: "Copy" });
  return (
    <section className={styles.panel} aria-labelledby={id}>
      <h2 id={id} className={styles.sectionHeading}>{t({ id: "detail.summary.title", message: "Overview" })}</h2>
      {facts.length === 0 ? (
        <p className={styles.empty}>{t({ id: "detail.summary.empty", message: "No details are available yet." })}</p>
      ) : (
        <dl className={styles.facts}>
          {facts.map(({ label, values }, index) => {
            const name = i18n._(label);
            return (
              <div key={`${label.id}-${index}`}>
                <dt>{name}</dt>
                {values.map(({ value, copy, link }, position) => {
                  const content = typeof value === "boolean"
                    ? (value ? t({ id: "common.yes", message: "Yes" }) : t({ id: "common.no", message: "No" }))
                    : typeof value === "object" ? i18n._(value) : <Ltr>{value}</Ltr>;
                  const to = link && projectName && typeof value === "string" && /^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$/.test(value)
                    ? `/p/${encodeURIComponent(projectName)}/${link}/${encodeURIComponent(value)}`
                    : undefined;
                  const text = typeof value === "string" || typeof value === "number" ? String(value) : undefined;
                  const copyName = values.length > 1 ? t({ id: "detail.copy.entry", message: `Copy ${name} ${position + 1}` }) : t({ id: "detail.copy.field", message: `Copy ${name}` });
                  return (
                    <dd key={position}>
                      <span className={styles.value}>{to ? <Link to={to}>{content}</Link> : content}</span>
                      {copy && text !== undefined ? (
                        <button type="button" className={`br-button br-button--secondary ${styles.copy}`} aria-label={copyName} onClick={async () => {
                          try {
                            await navigator.clipboard.writeText(text);
                            toast.show({ tone: "info", title: t({ id: "detail.copy.success", message: "Copied to clipboard" }) });
                          } catch {
                            toast.show({ tone: "danger", title: t({ id: "detail.copy.failure", message: "Could not copy to clipboard" }) });
                          }
                        }}>{copyLabel}</button>
                      ) : null}
                    </dd>
                  );
                })}
              </div>
            );
          })}
        </dl>
      )}
    </section>
  );
}

interface DetailExtras {
  children?: (object: KubeObject) => ReactNode;
  actions?: (object: KubeObject) => ReactNode;
}

function DetailBody({ def, namespace, name, projectName, children, actions }: { def: ResourceDef; namespace: string | undefined; name: string; projectName: string | undefined } & DetailExtras) {
  const { t, i18n } = useLingui();
  const ref = refOf(def);
  const query = useLiveObject(ref, namespace, name);
  const pendingWrites = useCurrentPendingWrites();
  const runner = useActionRunner(def);
  const [editing, setEditing] = useState(false);
  const checks = useMemo<Check[]>(
    () => [
      { verb: "update", group: def.group, resource: def.plural, name, ...(namespace === undefined ? {} : { namespace }) },
      ...def.actions.map((action) => ({
        verb: action.verb,
        group: def.group,
        resource: def.plural,
        name,
        ...(action.subresource === undefined ? {} : { subresource: action.subresource }),
        ...(namespace === undefined ? {} : { namespace }),
      })),
    ],
    [def, namespace, name],
  );
  const access = useCans(checks);
  const listPath = def.perspective === "admin" ? `/admin/${def.path}` : `/p/${projectName ?? ""}/${def.path}`;
  const crumbs = [{ label: i18n._(def.title), to: listPath }, { label: name }];

  if (query.isPending) {
    return <Spinner />;
  }
  if (query.isError) {
    return <><PageHeader title={name} breadcrumbs={crumbs} /><QueryFailure error={query.error} retry={() => void query.refetch()} /></>;
  }
  if (query.data === null || query.data === undefined) {
    return (
      <>
        <PageHeader title={name} breadcrumbs={crumbs} />
        <EmptyState title={t({ id: "detail.deleted.title", message: "This object was deleted" })} />
      </>
    );
  }
  const object: KubeObject = query.data;
  const pending = findPendingWrite(pendingWrites, ref, object);
  const kind = object.metadata.deletionTimestamp ? "delete" : pending?.kind;
  const conditions = Array.isArray(object.status?.conditions) ? (object.status.conditions as Condition[]) : [];
  const buttons = def.actions.flatMap((action, index) =>
    access[index + 1] === "allowed" ? [{ action, enabled: action.enabledWhen(object) }] : [],
  );

  return (
    <div className={styles.layout}>
      <PageHeader
        title={name}
        breadcrumbs={crumbs}
        actions={
          <>
            <div className={styles.health} role="status" aria-label={t({ id: "col.status", message: "Status" })}>
              <StatusBadge obj={object} />
              {kind ? <PendingWriteTag kind={kind} /> : null}
            </div>
            {!kind ? (
              <div className={styles.controls}>
                {actions?.(object)}
                {def.edit && access[0] === "allowed" ? (
                  <Button variant="secondary" onClick={() => setEditing(true)}>
                    {t({ id: "common.action.edit", message: "Edit" })}
                  </Button>
                ) : null}
                {buttons.map(({ action, enabled }) => (
                  <Button key={action.id} variant={action.kind === "delete" ? "danger" : "secondary"} disabled={!enabled} onClick={() => runner.request(action, object)}>
                    {i18n._(action.label)}
                  </Button>
                ))}
              </div>
            ) : null}
          </>
        }
      />
      <DetailFacts def={def} object={object} projectName={projectName} />
      {children?.(object)}
      {conditions.length > 0 ? <ConditionsTable conditions={conditions} /> : null}
      <details className={`${styles.panel} ${styles.advanced}`}>
        <summary>
          {t({ id: "detail.spec.title", message: "Specification" })}
          <span>{t({ id: "detail.advanced", message: "Advanced" })}</span>
        </summary>
        <pre dir="ltr" className={`br-code-block ${styles.specification}`} tabIndex={0}>
          <code>{specificationJSON(def.plural, object.spec)}</code>
        </pre>
      </details>
      {runner.dialogs}
      {editing ? <LazyEditDialog def={def} object={object} onClose={() => setEditing(false)} /> : null}
    </div>
  );
}

export function ResourceDetailPage({ def, ...extras }: { def: ResourceDef } & DetailExtras) {
  const { name = "" } = useParams();
  const projects = useProjects();
  const projectName = useCurrentProjectName();
  const project = projects.data?.find((candidate) => candidate.name === projectName);
  if (def.perspective === "project") {
    if (projects.isPending) {
      return <Spinner />;
    }
    if (!project) {
      return <NotFoundPage />;
    }
  }
  return <DetailBody def={def} namespace={def.scope === "Namespaced" ? project?.namespace : undefined} name={name} projectName={project?.name} {...extras} />;
}
