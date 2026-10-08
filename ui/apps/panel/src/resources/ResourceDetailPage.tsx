import { useMemo } from "react";
import { useParams } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Button, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow } from "@bedrock/design";
import { isHttpError } from "../api/http";
import type { Condition, KubeObject } from "../api/types";
import { useProjects } from "../api/projects";
import { useCans, type Check } from "../authz/authz";
import { EmptyState } from "../components/EmptyState";
import { Ltr } from "../components/Ltr";
import { PageHeader } from "../components/PageHeader";
import { Spinner } from "../components/Spinner";
import { StatusBadge } from "../components/StatusBadge";
import { NotFoundPage } from "../pages/NotFoundPage";
import { useCurrentProjectName } from "../shell/context";
import { useLiveObject } from "../stream/live";
import { refOf, type ResourceDef } from "./registry";
import { useActionRunner } from "./useActionRunner";

function ConditionsTable({ conditions }: { conditions: Condition[] }) {
  const { t } = useLingui();
  return (
    <Table caption={t({ id: "detail.conditions.title", message: "Conditions" })}>
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
  );
}

function DetailBody({ def, namespace, name, projectName }: { def: ResourceDef; namespace: string | undefined; name: string; projectName: string | undefined }) {
  const { t, i18n } = useLingui();
  const query = useLiveObject(refOf(def), namespace, name);
  const runner = useActionRunner(def);
  const checks = useMemo<Check[]>(
    () =>
      def.actions.map((action) => ({
        verb: action.verb,
        group: def.group,
        resource: def.plural,
        name,
        ...(action.subresource === undefined ? {} : { subresource: action.subresource }),
        ...(namespace === undefined ? {} : { namespace }),
      })),
    [def, namespace, name],
  );
  const access = useCans(checks);
  const listPath = def.perspective === "admin" ? `/admin/${def.path}` : `/p/${projectName ?? ""}/${def.path}`;
  const crumbs = [{ label: i18n._(def.title), to: listPath }, { label: name }];

  if (query.isPending) {
    return <Spinner />;
  }
  if (isHttpError(query.error) && query.error.status === 403) {
    return <EmptyState title={t({ id: "list.forbidden.title", message: "You cannot view this" })} />;
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
  const conditions = Array.isArray(object.status?.conditions) ? (object.status.conditions as Condition[]) : [];
  const buttons = def.actions.flatMap((action, index) =>
    access[index] === "allowed" ? [{ action, enabled: action.enabledWhen(object) }] : [],
  );

  return (
    <>
      <PageHeader
        title={name}
        breadcrumbs={crumbs}
        actions={buttons.map(({ action, enabled }) => (
          <Button key={action.id} variant={action.kind === "delete" ? "danger" : "secondary"} disabled={!enabled} onClick={() => runner.request(action, object)}>
            {i18n._(action.label)}
          </Button>
        ))}
      />
      <section className="br-stack">
        <StatusBadge obj={object} />
      </section>
      {conditions.length > 0 ? <ConditionsTable conditions={conditions} /> : null}
      <section>
        <h2>{t({ id: "detail.spec.title", message: "Specification" })}</h2>
        <pre dir="ltr" className="br-code-block">
          {JSON.stringify(object.spec ?? {}, null, 2)}
        </pre>
      </section>
      {runner.dialogs}
    </>
  );
}

export function ResourceDetailPage({ def }: { def: ResourceDef }) {
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
  return <DetailBody def={def} namespace={def.scope === "Namespaced" ? project?.namespace : undefined} name={name} projectName={project?.name} />;
}
