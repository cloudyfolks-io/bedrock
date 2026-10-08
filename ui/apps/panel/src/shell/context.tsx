import { useEffect, type ReactNode } from "react";
import { Navigate, useLocation, useMatch } from "react-router";
import { useSession } from "../api/session";
import { useProjects, type Project } from "../api/projects";
import { readStored, writeStored } from "../storage";
import type { Perspective } from "./navigation";

export function usePerspective(): { perspective: Perspective; setPerspective: (perspective: Perspective) => void } {
  const { pathname } = useLocation();
  const perspective: Perspective = pathname === "/admin" || pathname.startsWith("/admin/") ? "admin" : pathname.startsWith("/p") ? "project" : (readStored("perspective") === "admin" ? "admin" : "project");
  useEffect(() => {
    if (pathname.startsWith("/admin") || pathname.startsWith("/p")) {
      writeStored("perspective", perspective);
    }
  }, [pathname, perspective]);
  return { perspective, setPerspective: (next) => writeStored("perspective", next) };
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
