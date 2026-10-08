import { useMemo, useState } from "react";
import { Link } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Button, MenuButton, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow, TextField } from "@bedrock/design";
import { isHttpError } from "../api/http";
import type { KubeObject } from "../api/types";
import { useProjects, type Project } from "../api/projects";
import { useCans, type Check } from "../authz/authz";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { Spinner } from "../components/Spinner";
import { useLocale } from "../LocaleProvider";
import { NotFoundPage } from "../pages/NotFoundPage";
import { useCurrentProjectName } from "../shell/context";
import { useLiveList, type LiveState } from "../stream/live";
import { refOf, type ResourceDef } from "./registry";
import { useActionRunner } from "./useActionRunner";
import type { ColumnContext } from "./columns";

type SortState = { id: string; direction: "ascending" | "descending" };

function detailPathFor(def: ResourceDef, project: Project | undefined) {
  return (object: KubeObject) => (def.perspective === "admin" ? `/admin/${def.path}/${object.metadata.name}` : project ? `/p/${project.name}/${def.path}/${object.metadata.name}` : undefined);
}

function checksFor(def: ResourceDef, namespace: string | undefined): Check[] {
  const base = { group: def.group, resource: def.plural, ...(namespace === undefined ? {} : { namespace }) };
  return [
    { ...base, verb: "create" },
    { ...base, verb: "update" },
    ...def.actions.map((action) => ({ ...base, verb: action.verb, ...(action.subresource === undefined ? {} : { subresource: action.subresource }) })),
  ];
}

function compare(left: KubeObject, right: KubeObject, sort: SortState, def: ResourceDef): number {
  const column = def.columns.find((candidate) => candidate.id === sort.id);
  const a = column?.sortValue?.(left) ?? "";
  const b = column?.sortValue?.(right) ?? "";
  const order = typeof a === "number" && typeof b === "number" ? a - b : String(a).localeCompare(String(b));
  return sort.direction === "ascending" ? order : -order;
}

function LiveIndicator({ live }: { live: LiveState }) {
  const { t } = useLingui();
  const label = {
    live: t({ id: "common.live.live", message: "Live" }),
    connecting: t({ id: "common.live.connecting", message: "Connecting" }),
    paused: t({ id: "common.live.paused", message: "Paused" }),
  }[live];
  return (
    <span className="br-live" data-live={live} role="status">
      {label}
    </span>
  );
}

function ListBody({ def, namespace, project }: { def: ResourceDef; namespace: string | undefined; project: Project | undefined }) {
  const { t, i18n } = useLingui();
  const { locale } = useLocale();
  const query = useLiveList(refOf(def), namespace === undefined ? {} : { namespace });
  const checks = useMemo(() => checksFor(def, namespace), [def, namespace]);
  const access = useCans(checks);
  const runner = useActionRunner(def);
  const [filter, setFilter] = useState("");
  const [sort, setSort] = useState<SortState>({ id: "name", direction: "ascending" });
  const [, setCreating] = useState(false);
  const canCreate = def.create && access[0] === "allowed";
  const context: ColumnContext = { locale, now: new Date(), detailPath: detailPathFor(def, project) };
  const title = i18n._(def.title);

  const items = useMemo(() => {
    const all = query.data?.items ?? [];
    const scoped = def.perspective === "project" && def.scope === "Cluster" && project ? all.filter((item) => item.spec?.tenantRef === project.tenant) : all;
    const needle = filter.trim().toLowerCase();
    return scoped.filter((item) => item.metadata.name.toLowerCase().includes(needle)).sort((left, right) => compare(left, right, sort, def));
  }, [query.data, filter, sort, def, project]);

  const createButton = !canCreate ? null : def.createPath && project ? (
    <Link className="br-button br-button--primary" to={`/p/${project.name}/${def.createPath}`}>
      {t({ id: "common.action.create", message: "Create" })}
    </Link>
  ) : (
    <Button variant="primary" onClick={() => setCreating(true)}>
      {t({ id: "common.action.create", message: "Create" })}
    </Button>
  );

  const breadcrumbs = project ? [{ label: project.displayName || project.name }, { label: title }] : undefined;
  const header = <PageHeader title={title} {...(breadcrumbs ? { breadcrumbs } : {})} actions={createButton} />;

  if (query.isPending) {
    return (
      <>
        {header}
        <Spinner />
      </>
    );
  }
  if (isHttpError(query.error) && query.error.status === 403) {
    return (
      <>
        {header}
        <EmptyState title={t({ id: "list.forbidden.title", message: "You cannot view this" })} description={t({ id: "list.forbidden.description", message: "Ask a project owner for access." })} />
      </>
    );
  }
  if (query.isError) {
    return (
      <>
        {header}
        <EmptyState
          title={t({ id: "list.error.title", message: "The list did not load" })}
          action={
            <Button variant="primary" onClick={() => void query.refetch()}>
              {t({ id: "common.action.retry", message: "Retry" })}
            </Button>
          }
        />
      </>
    );
  }

  function rowItems(object: KubeObject) {
    const actionItems = def.actions.flatMap((action, index) =>
      access[index + 2] === "allowed"
        ? [
            {
              id: action.id,
              label: i18n._(action.label),
              disabled: !action.enabledWhen(object),
              tone: action.kind === "delete" ? ("danger" as const) : ("neutral" as const),
              onSelect: () => runner.request(action, object),
            },
          ]
        : [],
    );
    return actionItems;
  }

  const empty = (query.data?.items.length ?? 0) === 0;
  return (
    <>
      {header}
      <div className="br-toolbar">
        <TextField label={t({ id: "list.filter.label", message: "Filter by name" })} name="filter" value={filter} onChange={setFilter} />
        <LiveIndicator live={query.live} />
      </div>
      {empty ? (
        <EmptyState title={t({ id: "list.empty.title", message: "Nothing here yet" })} description={t({ id: "list.empty.description", message: "Objects of this kind appear here." })} action={createButton} />
      ) : items.length === 0 ? (
        <EmptyState title={t({ id: "list.noMatch.title", message: "No match" })} description={t({ id: "list.noMatch.description", message: "No object has this name." })} />
      ) : (
        <Table caption={title}>
          <TableHead>
            <TableRow>
              {def.columns.map((column) => (
                <TableHeaderCell
                  key={column.id}
                  sort={column.sortValue ? (sort.id === column.id ? sort.direction : "none") : undefined}
                  {...(column.sortValue
                    ? { onSort: () => setSort({ id: column.id, direction: sort.id === column.id && sort.direction === "ascending" ? "descending" : "ascending" }) }
                    : {})}
                >
                  {i18n._(column.label)}
                </TableHeaderCell>
              ))}
              <TableHeaderCell>
                <span className="br-visually-hidden">{t({ id: "list.actions.header", message: "Actions" })}</span>
              </TableHeaderCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {items.map((object) => {
              const menu = rowItems(object);
              return (
                <TableRow key={object.metadata.uid ?? object.metadata.name}>
                  {def.columns.map((column) => (
                    <TableCell key={column.id}>{column.render(object, context)}</TableCell>
                  ))}
                  <TableCell>{menu.length > 0 ? <MenuButton label={t({ id: "list.row.actions", message: `Actions for ${object.metadata.name}` })} items={menu} /> : null}</TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      )}
      {runner.dialogs}
    </>
  );
}

export function ResourceListPage({ def }: { def: ResourceDef }) {
  const projects = useProjects();
  const name = useCurrentProjectName();
  const project = projects.data?.find((candidate) => candidate.name === name);
  if (def.perspective === "admin") {
    return <ListBody def={def} namespace={undefined} project={undefined} />;
  }
  if (projects.isPending) {
    return <Spinner />;
  }
  if (!project) {
    return <NotFoundPage />;
  }
  return <ListBody def={def} namespace={def.scope === "Namespaced" ? project.namespace : undefined} project={project} />;
}
