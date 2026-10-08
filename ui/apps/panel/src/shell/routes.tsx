import { Navigate, Outlet, Route, Routes, useParams } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { msg } from "@lingui/core/macro";
import type { MessageDescriptor } from "@lingui/core";
import { useProjects } from "../api/projects";
import { EmptyState } from "../components/EmptyState";
import { Spinner } from "../components/Spinner";
import { AccountPage } from "../pages/AccountPage";
import { NotFoundPage } from "../pages/NotFoundPage";
import { PlaceholderPage } from "../pages/PlaceholderPage";
import { readStored, writeStored } from "../storage";
import { useEffect } from "react";
import { AdminOnly } from "./context";
import { Shell } from "./Shell";
import { navigationFor } from "./navigation";

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

interface PagePath {
  path: string;
  title: MessageDescriptor;
}

function pagesOf(perspective: "admin" | "project"): PagePath[] {
  return navigationFor(perspective, ":project").flatMap((section) => section.items.map((item) => ({ path: item.id, title: item.label })));
}

const detailPaths: Record<"admin" | "project", string[]> = {
  admin: ["hosts", "tenants", "cluster-images"],
  project: ["vms", "volumes", "images"],
};

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
          {pagesOf("admin").map((page) => (
            <Route key={page.path} path={page.path} element={<PlaceholderPage title={page.title} />} />
          ))}
          {detailPaths.admin.map((path) => (
            <Route key={path} path={`${path}/:name`} element={<PlaceholderPage title={msg({ id: "shell.detail.title", message: "Details" })} />} />
          ))}
        </Route>
        <Route path="p">
          <Route index element={<ProjectIndex />} />
          <Route path=":project" element={<ProjectScope />}>
            <Route index element={<Navigate to="vms" replace />} />
            {pagesOf("project").map((page) => (
              <Route key={page.path} path={page.path} element={<PlaceholderPage title={page.title} />} />
            ))}
            {detailPaths.project.map((path) => (
              <Route key={path} path={`${path}/:name`} element={<PlaceholderPage title={msg({ id: "shell.detail.title", message: "Details" })} />} />
            ))}
          </Route>
        </Route>
        <Route path="account" element={<AccountPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  );
}
