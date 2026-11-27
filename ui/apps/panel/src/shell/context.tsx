import { useEffect, type ReactNode } from "react";
import { Navigate, useMatch } from "react-router";
import { useSession } from "../api/session";
import { useProjects, type Project } from "../api/projects";
import { readStored, writeStored } from "../storage";

export function useRememberScope() {
  const admin = useMatch("/admin/*");
  const project = useMatch("/p/*");
  const scope = admin ? "admin" : project ? "project" : undefined;
  useEffect(() => {
    if (scope) {
      writeStored("perspective", scope);
    }
  }, [scope]);
}

export function useCurrentProjectName(): string | undefined {
  const match = useMatch({ path: "/p/:project", end: false });
  return match?.params.project ?? readStored("project") ?? undefined;
}

export function useCurrentProject(): Project | undefined {
  const name = useCurrentProjectName();
  const projects = useProjects().data;
  return projects?.find((project) => project.name === name);
}

export function AdminOnly({ children }: { children: ReactNode }) {
  const admin = useSession().data?.admin === true;
  return admin ? <>{children}</> : <Navigate to="/p" replace />;
}
