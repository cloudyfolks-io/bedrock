import { useId, useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Button, MenuButton, Select, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow, TextField } from "@bedrock/design";
import { isHttpError } from "../api/http";
import { findPendingWrite } from "../api/pendingWrites";
import type { KubeObject } from "../api/types";
import { useProjects, type Project } from "../api/projects";
import { useCans, type Check } from "../authz/authz";
import { EmptyState } from "../components/EmptyState";
import { Ltr } from "../components/Ltr";
import { PageHeader } from "../components/PageHeader";
import { PendingWriteTag, useCurrentPendingWrites } from "../components/PendingWrites";
import { Spinner } from "../components/Spinner";
import { useLocale } from "../LocaleProvider";
import { formatNumber } from "../format";
import { NotFoundPage } from "../pages/NotFoundPage";
import { useCurrentProjectName } from "../shell/context";
import { useLiveList, type LiveState } from "../stream/live";
import { refOf, type ResourceDef } from "./registry";
import { useActionRunner } from "./useActionRunner";
import type { ColumnContext, ColumnDef } from "./columns";
import { CompactResourceRow, CompactResourceRows, useCompactResourceRows } from "./CompactResourceRows";
import { LazyCreateDialog, LazyEditDialog } from "../forms/LazyDialogs";
import styles from "./resources.module.css";

type SortState = { id: string; direction: "ascending" | "descending" };
const initialSort: SortState = { id: "name", direction: "ascending" };

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
    <span className={`br-live ${styles.live}`} data-live={live} role="status">
      {label}
    </span>
  );
}

interface ListExtras {
  help?: ReactNode;
  toolbar?: ReactNode;
  actions?: ReactNode | ((context: { canCreate: boolean; project: Project | undefined }) => ReactNode);
  itemFilter?: (object: KubeObject) => boolean;
  listPath?: string;
  listFailure?: (error: unknown) => ReactNode;
  readOnly?: boolean;
  embedded?: boolean;
  hideEmbeddedTitle?: boolean;
  forbidden?: ReactNode;
  createSpec?: Record<string, unknown>;
  lockedPaths?: string[];
  elasticIps?: KubeObject[];
  rowItems?: (object: KubeObject) => { id: string; label: string; onSelect: () => void; disabled?: boolean }[];
  children?: ReactNode;
  renderFiltered?: (items: readonly KubeObject[]) => ReactNode;
}

function ListBody({ def, namespace, project, help = null, toolbar = null, actions = null, itemFilter, listPath, listFailure, readOnly = false, embedded = false, hideEmbeddedTitle = false, forbidden, createSpec, lockedPaths, elasticIps, rowItems: extraRowItems, children = null, renderFiltered }: { def: ResourceDef; namespace: string | undefined; project: Project | undefined } & ListExtras) {
  const { t, i18n } = useLingui();
  const { locale } = useLocale();
  const ref = refOf(def);
  const tenantScopedClusterList = def.perspective === "project" && !ref.namespaced;
  const query = useLiveList(ref, {
    ...(namespace === undefined ? {} : { namespace }),
    ...(listPath === undefined ? {} : { listPath }),
    optimisticCreates: !tenantScopedClusterList,
  });
  const pendingWrites = useCurrentPendingWrites();
  const checks = useMemo(() => checksFor(def, namespace), [def, namespace]);
  const access = useCans(checks);
  const runner = useActionRunner(def);
  const [filter, setFilter] = useState("");
  const [selection, setSort] = useState(initialSort);
  const sortColumns = def.columns.filter((column) => column.sortValue);
  const sort = sortColumns.some((column) => column.id === selection.id) ? selection : initialSort;
  const rowPrefix = useId();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<KubeObject | undefined>();
  const compact = useCompactResourceRows();
  const canCreate = !readOnly && def.create && access[0] === "allowed";
  const context: ColumnContext = { locale, now: new Date(), detailPath: readOnly ? () => undefined : detailPathFor(def, project), ...(project ? { project: project.name } : {}), ...(elasticIps ? { elasticIps } : {}) };
  const title = i18n._(def.title);

  const items = useMemo(() => {
    const all = query.data?.items ?? [];
    const scoped = tenantScopedClusterList && project ? all.filter((item) => item.spec?.tenantRef === project.tenant) : all;
    const needle = filter.trim().toLowerCase();
    return scoped.filter((item) => itemFilter?.(item) ?? true).filter((item) => item.metadata.name.toLowerCase().includes(needle)).sort((left, right) => compare(left, right, sort, def));
  }, [query.data, filter, sort, def, project, itemFilter, tenantScopedClusterList]);

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
  const header = embedded ? hideEmbeddedTitle && !createButton ? null : (
    <div className={`br-page-title-row ${styles.embeddedHeader}`}>
      {hideEmbeddedTitle ? null : <h2>{title}</h2>}
      {createButton}
    </div>
  ) : (
    <>
      <PageHeader title={title} {...(breadcrumbs ? { breadcrumbs } : {})} actions={<>{typeof actions === "function" ? actions({ canCreate, project }) : actions}{createButton}</>} />
      {help}
    </>
  );

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
        {forbidden ?? <EmptyState title={t({ id: "list.forbidden.title", message: "You cannot view this" })} description={t({ id: "list.forbidden.description", message: "Ask a project owner for access." })} />}
      </>
    );
  }
  if (query.isError) {
    return (
      <>
        {header}
        {listFailure?.(query.error) ?? <EmptyState
          title={t({ id: "list.error.title", message: "The list did not load" })}
          action={
            <Button variant="primary" onClick={() => void query.refetch()}>
              {t({ id: "common.action.retry", message: "Retry" })}
            </Button>
          }
        />}
      </>
    );
  }

  function rowItems(object: KubeObject, labelId: string) {
    const actionItems = def.actions.flatMap((action, index) =>
      access[index + 2] === "allowed"
        ? [
            {
              id: action.id,
              label: i18n._(action.label),
              disabled: !action.enabledWhen(object),
              tone: action.kind === "delete" ? ("danger" as const) : ("neutral" as const),
              onSelect: () => {
                runner.request(action, object);
                if (action.confirm === "none") {
                  requestAnimationFrame(() => {
                    const label = document.getElementById(labelId);
                    if (document.activeElement === document.body) (label?.querySelector("a") ?? label)?.focus();
                  });
                }
              },
            },
          ]
        : [],
    );
    if (readOnly) {
      return [];
    }
    const editItem = def.edit && access[1] === "allowed" ? [{ id: "edit", label: t({ id: "common.action.edit", message: "Edit" }), tone: "neutral" as const, onSelect: () => setEditing(object) }] : [];
    return [...editItem, ...(extraRowItems?.(object).map((entry) => ({ ...entry, tone: "neutral" as const })) ?? []), ...actionItems];
  }

  const empty = (query.data?.items.length ?? 0) === 0;
  const rowCount = formatNumber(items.length, locale);
  const nameColumn = def.columns.find((column) => column.id === "name");
  const stateColumn = def.columns.find((column) => column.id === "status" || column.id === "phase");

  function renderRow(object: KubeObject) {
    const pending = findPendingWrite(pendingWrites, ref, object);
    const kind = object.metadata.deletionTimestamp ? "delete" : pending?.kind;
    const provisional = pending?.kind === "create" && !object.metadata.uid;
    const key = object.metadata.uid ?? `${object.metadata.namespace ?? ""}/${object.metadata.name}`;
    const labelId = `${rowPrefix}-${key}`;
    const menu = kind ? [] : rowItems(object, labelId);
    const actions = menu.length > 0 ? <MenuButton label={t({ id: "list.row.actions", message: `Actions for ${object.metadata.name}` })} items={menu} /> : null;
    function cell(column: ColumnDef) {
      const content = (
        <>
          {provisional && column.id === "name" ? <Ltr>{object.metadata.name}</Ltr> : null}
          {!provisional ? column.render(object, context) : null}
          {column.id === "name" && kind ? <PendingWriteTag kind={kind} /> : null}
        </>
      );
      return column.id === "name" ? <span id={labelId} tabIndex={-1}>{content}</span> : content;
    }
    return compact ? (
      <CompactResourceRow key={key} name={nameColumn ? cell(nameColumn) : object.metadata.name} status={stateColumn ? cell(stateColumn) : null} actions={actions}
        fields={provisional ? [] : def.columns.filter((column) => column !== nameColumn && column !== stateColumn)
          .map((column) => ({ id: column.id, label: i18n._(column.label), value: column.render(object, context) }))} />
    ) : (
      <TableRow key={key}>
        {def.columns.map((column) => <TableCell key={column.id}>{cell(column)}</TableCell>)}
        <TableCell>{actions}</TableCell>
      </TableRow>
    );
  }

  return (
    <>
      {header}
      <div className={`${styles.panel} ${styles.listPanel}`}>
        <div className={styles.toolbar}>
          <div className={styles.filters}>
            <TextField label={t({ id: "list.filter.label", message: "Filter by name" })} name="filter" value={filter} onChange={setFilter} />
            {toolbar}
            {compact && sortColumns.length > 0 && items.length > 1 ? (
              <div className={styles.compactSort}>
                <Select label={t({ id: "list.sortBy", message: "Sort by" })} value={sort.id}
                  options={sortColumns.map((column) => ({ value: column.id, label: i18n._(column.label) }))}
                  onValueChange={(id) => setSort({ id, direction: "ascending" })} />
                <Button variant="secondary" onClick={() => setSort({ ...sort, direction: sort.direction === "ascending" ? "descending" : "ascending" })}>
                  <span className="br-visually-hidden">{sort.direction === "ascending"
                    ? t({ id: "list.sortDescending", message: "Sort descending" })
                    : t({ id: "list.sortAscending", message: "Sort ascending" })}</span>
                  <span aria-hidden="true">{sort.direction === "ascending" ? "↑" : "↓"}</span>
                </Button>
              </div>
            ) : null}
          </div>
          <div className={styles.listMeta}>
            <span>{t({ id: "list.resultCount", message: `${rowCount} shown` })}</span>
            {!listPath ? <LiveIndicator live={query.live} /> : null}
          </div>
        </div>
        {empty ? (
          <EmptyState title={t({ id: "list.empty.title", message: "Nothing here yet" })} description={t({ id: "list.empty.description", message: "Objects of this kind appear here." })} action={createButton} />
        ) : items.length === 0 ? (
          <EmptyState title={t({ id: "list.noMatch.title", message: "No match" })} description={t({ id: "list.noMatch.description", message: "No object has this name." })} />
        ) : compact ? (
          <CompactResourceRows label={title}>{items.map(renderRow)}</CompactResourceRows>
        ) : (
          <Table caption={title}>
            <TableHead>
              <TableRow>
                {def.columns.map((column) => {
                  const direction = sort.id === column.id ? sort.direction : "none";
                  return (
                    <TableHeaderCell
                      key={column.id}
                      sort={column.sortValue ? direction : undefined}
                      {...(column.sortValue
                        ? { onSort: () => setSort({ id: column.id, direction: direction === "ascending" ? "descending" : "ascending" }) }
                        : {})}
                    >
                      {i18n._(column.label)}
                      {column.sortValue ? <span aria-hidden="true" className={styles.sortIcon}>{direction === "ascending" ? "↑" : direction === "descending" ? "↓" : "↕"}</span> : null}
                    </TableHeaderCell>
                  );
                })}
                <TableHeaderCell>
                  <span className="br-visually-hidden">{t({ id: "list.actions.header", message: "Actions" })}</span>
                </TableHeaderCell>
              </TableRow>
            </TableHead>
            <TableBody>{items.map(renderRow)}</TableBody>
          </Table>
        )}
      </div>
      {items.length > 0 ? renderFiltered?.(items) : null}
      {runner.dialogs}
      {children}
      {creating ? <LazyCreateDialog def={def} {...(createSpec ? { initialSpec: createSpec } : {})} {...(lockedPaths ? { lockedPaths } : {})} onClose={() => setCreating(false)} /> : null}
      {editing ? <LazyEditDialog def={def} object={editing} onClose={() => setEditing(undefined)} /> : null}
    </>
  );
}

export function ResourceListPage({ def, ...extras }: { def: ResourceDef } & ListExtras) {
  const projects = useProjects();
  const name = useCurrentProjectName();
  const project = projects.data?.find((candidate) => candidate.name === name);
  if (def.perspective === "project") {
    if (projects.isPending) {
      return <Spinner />;
    }
    if (!project) {
      return <NotFoundPage />;
    }
  }
  const scopeProject = def.perspective === "project" ? project : undefined;
  return (
    <div className={styles.layout} data-embedded={extras.embedded || undefined}>
      <ListBody def={def} namespace={def.scope === "Namespaced" ? scopeProject?.namespace : undefined} project={scopeProject} {...extras} />
    </div>
  );
}
