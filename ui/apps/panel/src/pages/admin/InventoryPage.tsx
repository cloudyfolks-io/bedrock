import { useId, useState, type FormEvent } from "react";
import { msg } from "@lingui/core/macro";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Button, Select, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow, TextField } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { isHttpError } from "../../api/http";
import { resourcePath } from "../../api/kube";
import { projectOptions, useProjects, type Project } from "../../api/projects";
import type { KubeList } from "../../api/types";
import { EmptyState } from "../../components/EmptyState";
import { Ltr } from "../../components/Ltr";
import { PageHeader } from "../../components/PageHeader";
import { QueryFailure } from "../../components/QueryFailure";
import { Spinner } from "../../components/Spinner";
import { StatusBadge } from "../../components/StatusBadge";
import { formatNumber } from "../../format";
import { useLocale } from "../../LocaleProvider";
import { CompactResourceRow, CompactResourceRows, useCompactResourceRows } from "../../resources/CompactResourceRows";
import { textAt } from "../../resources/path";
import { definitionsFor, refOf, type ResourceDef } from "../../resources/registry";
import styles from "./inventory.module.css";

const restorePlural = "virtualmachinerestores";
const inventoryKinds = definitionsFor("project").map((def) => def.plural === restorePlural
  ? { ...def, path: "restores", title: msg({ id: "inventory.vmRestores", message: "Virtual machine restores" }) }
  : def);
const allProjects = "__all__";
const pageSize = "50";

function inventoryPath(def: ResourceDef, namespace: string | undefined, name: string, cursor: string): string {
  const query: Record<string, string> = { limit: pageSize };
  if (name) {
    query.fieldSelector = `metadata.name=${name.replace(/[\\,=]/g, "\\$&")}`;
  }
  if (cursor) {
    query.continue = cursor;
  }
  return resourcePath(refOf(def), namespace ? { namespace } : {}, query);
}

function InventorySearch({ name, onSearch }: { name: string; onSearch: (name: string) => void }) {
  const { t } = useLingui();
  const [edit, setEdit] = useState({ base: name, value: name });
  const draft = edit.base === name ? edit.value : name;

  function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    onSearch(draft.trim());
  }

  return (
    <form className={styles.search} onSubmit={search}>
      <TextField label={t({ id: "inventory.name", message: "Exact resource name" })} name="name" value={draft} onChange={(value) => setEdit({ base: name, value })} />
      <Button type="submit" variant="secondary">{t({ id: "inventory.search", message: "Search" })}</Button>
    </form>
  );
}

function InventoryList({ def, projects, project, name }: { def: ResourceDef; projects: Project[]; project: Project | undefined; name: string }) {
  const { t, i18n } = useLingui();
  const { locale } = useLocale();
  const http = useHttp();
  const compact = useCompactResourceRows();
  const instance = useId();
  const [page, setPage] = useState(0);
  const [traversal, setTraversal] = useState(0);
  const namespace = def.scope === "Namespaced" ? project?.namespace : undefined;
  const query = useInfiniteQuery({
    queryKey: ["admin-inventory", inventoryPath(def, namespace, name, ""), instance, traversal],
    queryFn: ({ pageParam, signal }) => http.request<KubeList>("GET", inventoryPath(def, namespace, name, pageParam), { signal }),
    initialPageParam: "",
    getNextPageParam: (last) => last.metadata.continue || undefined,
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const byNamespace = new Map(projects.filter((candidate) => candidate.namespace).map((candidate) => [candidate.namespace, candidate]));
  const projectLabels = new Map(projectOptions(projects).map((option) => [option.value, option.label]));
  const visiblePage = Math.min(page, Math.max(0, (query.data?.pages.length ?? 1) - 1));
  const current = query.data?.pages[visiblePage];
  const next = current?.metadata.continue;
  const items = current?.items ?? [];
  const pageNumber = visiblePage + 1;
  const rowCount = formatNumber(items.length, locale);
  const title = i18n._(def.title);

  function firstPage() {
    setPage(0);
    setTraversal((value) => value + 1);
  }

  async function nextPage() {
    const index = visiblePage + 1;
    if (query.data?.pages[index]) {
      setPage(index);
      return;
    }
    const result = await query.fetchNextPage({ cancelRefetch: false });
    if (result.data?.pages[index]) {
      setPage(index);
    }
  }

  if (query.isPending) {
    return <Spinner />;
  }
  if (query.isError) {
    if (query.isFetchNextPageError && isHttpError(query.error) && query.error.status === 410) {
      return (
        <EmptyState
          title={t({ id: "inventory.expired", message: "This list expired" })}
          action={<Button variant="secondary" onClick={firstPage}>{t({ id: "inventory.first", message: "Go to first page" })}</Button>}
        />
      );
    }
    return <QueryFailure error={query.error} retry={() => void (query.isFetchNextPageError ? nextPage() : query.refetch())} />;
  }
  const rows = items.map((object) => {
    const resource = object.metadata;
    const owner = byNamespace.get(resource.namespace ?? "");
    const tenant = textAt(object, "spec.tenantRef");
    const detail = owner && (def.plural === restorePlural ? `/p/${owner.name}/backups` : `/p/${owner.name}/${def.path}/${resource.name}`);
    const name = detail ? <Link to={detail}><Ltr>{resource.name}</Ltr></Link> : <Ltr>{resource.name}</Ltr>;
    const status = <StatusBadge obj={object} />;
    const fields = def.scope === "Namespaced" ? [
      { id: "project", label: t({ id: "shell.project.label", message: "Project" }), value: owner ? <Link to={`/p/${owner.name}`}>{projectLabels.get(owner.name) ?? owner.name}</Link> : t({ id: "inventory.noProject", message: "No linked project" }) },
      { id: "namespace", label: t({ id: "col.namespace", message: "Namespace" }), value: <Ltr>{resource.namespace ?? "—"}</Ltr> },
    ] : [
      { id: "tenant", label: t({ id: "inventory.tenant", message: "Tenant" }), value: tenant ? <Link to={`/admin/tenants/${tenant}`}><Ltr>{tenant}</Ltr></Link> : "—" },
    ];
    const key = resource.uid ?? `${resource.namespace}/${resource.name}`;
    return compact ? <CompactResourceRow key={key} name={name} status={status} fields={fields} /> : (
      <TableRow key={key}>
        <TableCell>{name}</TableCell>
        {fields.map(({ id, value }) => <TableCell key={id}>{value}</TableCell>)}
        <TableCell>{status}</TableCell>
      </TableRow>
    );
  });
  return (
    <>
      <div className={styles.resultsHead}>
        <div>
          <h2>{title}</h2>
          <span>{t({ id: "inventory.rowsOnPage", message: `Rows on this page: ${rowCount}` })}</span>
        </div>
        <Button variant="secondary" disabled={query.isFetching} onClick={firstPage}>{t({ id: "inventory.refresh", message: "Refresh list" })}</Button>
      </div>
      {items.length === 0 ? (
        <EmptyState title={next || visiblePage > 0 ? t({ id: "inventory.emptyPage", message: "No resources match on this page" }) : t({ id: "inventory.empty", message: "No resources match" })} />
      ) : compact ? (
        <CompactResourceRows label={title}>{rows}</CompactResourceRows>
      ) : (
        <div className={styles.tableViewport}>
          <Table caption={title}>
            <TableHead>
              <TableRow>
                <TableHeaderCell>{t({ id: "col.name", message: "Name" })}</TableHeaderCell>
                {def.scope === "Namespaced" ? (
                  <>
                    <TableHeaderCell>{t({ id: "shell.project.label", message: "Project" })}</TableHeaderCell>
                    <TableHeaderCell>{t({ id: "col.namespace", message: "Namespace" })}</TableHeaderCell>
                  </>
                ) : (
                  <TableHeaderCell>{t({ id: "inventory.tenant", message: "Tenant" })}</TableHeaderCell>
                )}
                <TableHeaderCell>{t({ id: "col.status", message: "Status" })}</TableHeaderCell>
              </TableRow>
            </TableHead>
            <TableBody>{rows}</TableBody>
          </Table>
        </div>
      )}
      {next || visiblePage > 0 ? (
        <nav className={styles.pagination} aria-label={t({ id: "inventory.pages", message: "Inventory pages" })}>
          <Button variant="secondary" disabled={visiblePage === 0 || query.isFetching} onClick={() => setPage(visiblePage - 1)}>{t({ id: "inventory.previous", message: "Previous" })}</Button>
          <span role="status">{t({ id: "inventory.page", message: `Page ${pageNumber}` })}</span>
          <Button variant="secondary" disabled={!next || query.isFetching} onClick={() => void nextPage()}>{t({ id: "inventory.next", message: "Next" })}</Button>
        </nav>
      ) : null}
    </>
  );
}

export default function InventoryPage() {
  const { t, i18n } = useLingui();
  const projects = useProjects();
  const [params, setParams] = useSearchParams();
  const kind = params.get("kind") ?? "vms";
  const requestedProject = params.get("project") ?? allProjects;
  const name = params.get("name") ?? "";
  const def = inventoryKinds.find((candidate) => candidate.path === kind);
  const project = projects.data?.find((candidate) => candidate.name === requestedProject);
  const needsProject = def?.scope === "Namespaced" && requestedProject !== allProjects;
  const blockedProjectFilter = def?.scope === "Cluster" && requestedProject !== allProjects;

  function updateFilter(key: string, value: string) {
    const updated = new URLSearchParams(params);
    if (value) {
      updated.set(key, value);
    } else {
      updated.delete(key);
    }
    setParams(updated);
  }

  const ready = def && projects.isSuccess && !blockedProjectFilter && (!needsProject || project?.namespace);
  return (
    <div className={styles.inventory}>
      <PageHeader
        title={t({ id: "nav.item.inventory", message: "Resource inventory" })}
        description={t({ id: "inventory.subtitle", message: "Find resources across projects and tenants." })}
      />
      <div className={styles.results}>
        <div className={styles.filters}>
          <Select label={t({ id: "inventory.resourceType", message: "Resource type" })} value={def?.path ?? ""} options={inventoryKinds.map((candidate) => ({ value: candidate.path, label: i18n._(candidate.title) }))} onValueChange={(value) => updateFilter("kind", value)} />
          {def?.scope === "Namespaced" ? (
            <Select label={t({ id: "shell.project.label", message: "Project" })} value={project?.name ?? (needsProject ? "" : allProjects)} disabled={!projects.isSuccess} options={[{ value: allProjects, label: t({ id: "inventory.allProjects", message: "All projects" }) }, ...projectOptions(projects.data ?? [])]} onValueChange={(value) => updateFilter("project", value === allProjects ? "" : value)} />
          ) : null}
          <InventorySearch name={name} onSearch={(value) => updateFilter("name", value)} />
        </div>
        {projects.isPending ? <Spinner /> : null}
        {projects.isError ? <QueryFailure error={projects.error} retry={() => void projects.refetch()} /> : null}
        {!def ? <EmptyState title={t({ id: "inventory.unknownType", message: "Select a resource type" })} /> : null}
        {projects.isSuccess && needsProject && !project?.namespace ? (
          <EmptyState title={project ? t({ id: "inventory.projectPending", message: "The project namespace is not ready" }) : t({ id: "inventory.projectUnavailable", message: "This project is unavailable" })} />
        ) : null}
        {blockedProjectFilter ? (
          <EmptyState
            title={t({ id: "inventory.networkScope", message: "Networks use tenant scope" })}
            action={<Button variant="secondary" onClick={() => updateFilter("project", "")}>{t({ id: "inventory.clearProject", message: "Clear project filter" })}</Button>}
          />
        ) : null}
        {ready ? <InventoryList key={JSON.stringify([def.path, project?.name, project?.namespace, name])} def={def} projects={projects.data ?? []} project={project} name={name} /> : null}
      </div>
    </div>
  );
}
