import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
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
import { textAt } from "../../resources/path";
import { primaryDefinitionsFor, refOf, type ResourceDef } from "../../resources/registry";

const inventoryKinds = primaryDefinitionsFor("project");
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
    <form className="br-inventory-search" onSubmit={search}>
      <TextField label={t({ id: "inventory.name", message: "Exact resource name" })} name="name" value={draft} onChange={(value) => setEdit({ base: name, value })} />
      <Button type="submit" variant="secondary">{t({ id: "inventory.search", message: "Search" })}</Button>
    </form>
  );
}

function InventoryList({ def, projects, project, name }: { def: ResourceDef; projects: Project[]; project: Project | undefined; name: string }) {
  const { t, i18n } = useLingui();
  const http = useHttp();
  const [cursors, setCursors] = useState([""]);
  const cursor = cursors.at(-1) ?? "";
  const namespace = def.scope === "Namespaced" ? project?.namespace : undefined;
  const path = inventoryPath(def, namespace, name, cursor);
  const query = useQuery({
    queryKey: ["admin-inventory", path],
    queryFn: ({ signal }) => http.request<KubeList>("GET", path, { signal }),
    retry: false,
    refetchOnWindowFocus: false,
  });
  const byNamespace = new Map(projects.filter((candidate) => candidate.namespace).map((candidate) => [candidate.namespace, candidate]));
  const projectLabels = new Map(projectOptions(projects).map((option) => [option.value, option.label]));
  const next = query.data?.metadata.continue;
  const items = query.data?.items ?? [];
  const page = cursors.length;
  if (query.isPending) {
    return <Spinner />;
  }
  if (query.isError) {
    if (cursor && isHttpError(query.error) && query.error.status === 410) {
      return (
        <EmptyState
          title={t({ id: "inventory.expired", message: "This list expired" })}
          action={<Button variant="secondary" onClick={() => setCursors([""])}>{t({ id: "inventory.first", message: "Go to first page" })}</Button>}
        />
      );
    }
    return <QueryFailure error={query.error} retry={() => void query.refetch()} />;
  }
  return (
    <>
      {items.length === 0 ? (
        <EmptyState title={t({ id: "inventory.empty", message: "No resources match" })} />
      ) : (
        <Table caption={i18n._(def.title)}>
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
          <TableBody>
            {items.map((object) => {
              const resource = object.metadata;
              const owner = byNamespace.get(resource.namespace ?? "");
              const tenant = textAt(object, "spec.tenantRef");
              return (
                <TableRow key={resource.uid ?? `${resource.namespace}/${resource.name}`}>
                  <TableCell>{owner ? <Link to={`/p/${owner.name}/${def.path}/${resource.name}`}><Ltr>{resource.name}</Ltr></Link> : <Ltr>{resource.name}</Ltr>}</TableCell>
                  {def.scope === "Namespaced" ? (
                    <>
                      <TableCell>{owner ? <Link to={`/p/${owner.name}`}>{projectLabels.get(owner.name) ?? owner.name}</Link> : t({ id: "inventory.noProject", message: "No linked project" })}</TableCell>
                      <TableCell><Ltr>{resource.namespace ?? "—"}</Ltr></TableCell>
                    </>
                  ) : (
                    <TableCell>{tenant ? <Link to={`/admin/tenants/${tenant}`}><Ltr>{tenant}</Ltr></Link> : "—"}</TableCell>
                  )}
                  <TableCell><StatusBadge obj={object} /></TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      )}
      {next || cursors.length > 1 ? (
        <nav className="br-inventory-pages" aria-label={t({ id: "inventory.pages", message: "Inventory pages" })}>
          <Button variant="secondary" disabled={page === 1} onClick={() => setCursors((value) => value.slice(0, -1))}>{t({ id: "inventory.previous", message: "Previous" })}</Button>
          <span role="status">{t({ id: "inventory.page", message: `Page ${page}` })}</span>
          <Button variant="secondary" disabled={!next} onClick={() => { if (next) setCursors((value) => [...value, next]); }}>{t({ id: "inventory.next", message: "Next" })}</Button>
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

  function updateFilter(key: string, value: string) {
    const updated = new URLSearchParams(params);
    if (value) {
      updated.set(key, value);
    } else {
      updated.delete(key);
    }
    setParams(updated);
  }

  const ready = def && projects.isSuccess && (!needsProject || project?.namespace);
  return (
    <>
      <PageHeader title={t({ id: "nav.item.inventory", message: "Resource inventory" })} />
      <div className="br-toolbar br-inventory-toolbar">
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
      {ready ? <InventoryList key={JSON.stringify([def.path, project?.namespace, name])} def={def} projects={projects.data ?? []} project={project} name={name} /> : null}
    </>
  );
}
