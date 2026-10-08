import { lazy, type ReactElement } from "react";
import { Navigate, Outlet, Route, Routes, useParams } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { useProjects } from "../api/projects";
import { EmptyState } from "../components/EmptyState";
import { Spinner } from "../components/Spinner";
import { AccountPage } from "../pages/AccountPage";
import { NotFoundPage } from "../pages/NotFoundPage";
import { ResourceDetailPage } from "../resources/ResourceDetailPage";
import { ResourceListPage } from "../resources/ResourceListPage";
import { primaryDefinitionsFor, type ResourceDef } from "../resources/registry";
import { readStored, writeStored } from "../storage";
import { useEffect } from "react";
import { AdminOnly } from "./context";
import { Shell } from "./Shell";

const HostsPage = lazy(() => import("../pages/admin/HostsPage"));
const HostDetailPage = lazy(() => import("../pages/admin/HostDetailPage"));
const UpgradesPage = lazy(() => import("../pages/admin/UpgradesPage"));
const AddonsPage = lazy(() => import("../pages/admin/AddonsPage"));
const TenantDetailPage = lazy(() => import("../pages/admin/TenantDetailPage"));
const VmsPage = lazy(() => import("../pages/project/VmsPage"));
const VmDetailPage = lazy(() => import("../pages/project/VmDetailPage"));
const VolumesPage = lazy(() => import("../pages/project/VolumesPage"));
const SnapshotsPage = lazy(() => import("../pages/project/SnapshotsPage"));
const ImagesPage = lazy(() => import("../pages/project/ImagesPage"));
const BackupsPage = lazy(() => import("../pages/project/BackupsPage"));
const NetworksPage = lazy(() => import("../pages/project/NetworksPage"));
const ElasticIpsPage = lazy(() => import("../pages/project/ElasticIpsPage"));
const ObjectUsersPage = lazy(() => import("../pages/project/ObjectUsersPage"));
const SecurityGroupDetailPage = lazy(() => import("../pages/project/SecurityGroupDetailPage"));
const SettingsPage = lazy(() => import("../pages/admin/SettingsPage"));

const listPages: Record<string, (def: ResourceDef) => ReactElement> = {
  "admin/hosts": () => <HostsPage />,
  "admin/settings": () => <SettingsPage />,
  "admin/upgrades": () => <UpgradesPage />,
  "admin/addons": () => <AddonsPage />,
  "project/vms": () => <VmsPage />,
  "project/volumes": () => <VolumesPage />,
  "project/snapshots": () => <SnapshotsPage />,
  "project/images": () => <ImagesPage />,
  "project/backups": () => <BackupsPage />,
  "project/networks": () => <NetworksPage />,
  "project/elastic-ips": () => <ElasticIpsPage />,
  "project/object-users": () => <ObjectUsersPage />,
};

const detailPages: Record<string, (def: ResourceDef) => ReactElement> = {
  "admin/hosts": () => <HostDetailPage />,
  "admin/tenants": () => <TenantDetailPage />,
  "project/vms": () => <VmDetailPage />,
  "project/security-groups": () => <SecurityGroupDetailPage />,
};

function listPage(def: ResourceDef): ReactElement {
  return (listPages[`${def.perspective}/${def.path}`] ?? ((candidate) => <ResourceListPage def={candidate} />))(def);
}

function detailPage(def: ResourceDef): ReactElement {
  return (detailPages[`${def.perspective}/${def.path}`] ?? ((candidate) => <ResourceDetailPage def={candidate} />))(def);
}

function HomeRedirect() {
  return <Navigate to={readStored("perspective") === "admin" ? "/admin" : "/p"} replace />;
}

function ProjectIndex() {
  const { t } = useLingui();
  const projects = useProjects();
  if (projects.isPending) {
    return <Spinner />;
  }
  const list = projects.data ?? [];
  const stored = readStored("project");
  const target = list.find((project) => project.name === stored) ?? list[0];
  if (!target) {
    return (
      <EmptyState
        title={t({ id: "shell.project.none.title", message: "No projects" })}
        description={t({ id: "shell.project.none.description", message: "You are not a member of any project yet." })}
      />
    );
  }
  return <Navigate to={`/p/${target.name}/vms`} replace />;
}

function ProjectScope() {
  const { project = "" } = useParams();
  const projects = useProjects();
  const known = projects.data?.some((candidate) => candidate.name === project) === true;
  useEffect(() => {
    if (known) {
      writeStored("project", project);
    }
  }, [known, project]);
  if (projects.isPending) {
    return <Spinner />;
  }
  return known ? <Outlet /> : <NotFoundPage />;
}

export function AppRoutes() {
  return (
    <Routes>
      <Route element={<Shell />}>
        <Route index element={<HomeRedirect />} />
        <Route
          path="admin"
          element={
            <AdminOnly>
              <Outlet />
            </AdminOnly>
          }
        >
          <Route index element={<Navigate to="/admin/hosts" replace />} />
          {primaryDefinitionsFor("admin").flatMap((def) => [
            <Route key={def.path} path={def.path} element={listPage(def)} />,
            <Route key={`${def.path}/:name`} path={`${def.path}/:name`} element={detailPage(def)} />,
          ])}
        </Route>
        <Route path="p">
          <Route index element={<ProjectIndex />} />
          <Route path=":project" element={<ProjectScope />}>
            <Route index element={<Navigate to="vms" replace />} />
            {primaryDefinitionsFor("project").flatMap((def) => [
              <Route key={def.path} path={def.path} element={listPage(def)} />,
              <Route key={`${def.path}/:name`} path={`${def.path}/:name`} element={detailPage(def)} />,
            ])}
          </Route>
        </Route>
        <Route path="account" element={<AccountPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  );
}
