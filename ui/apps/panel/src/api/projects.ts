import { useQuery } from "@tanstack/react-query";
import { useHttp } from "./HttpProvider";

export interface Project {
  name: string;
  displayName: string;
  tenant: string;
  namespace: string;
}

export function projectOptions(projects: readonly Project[]) {
  const labels = new Map<string, number>();
  for (const project of projects) {
    const label = project.displayName || project.name;
    labels.set(label, (labels.get(label) ?? 0) + 1);
  }
  return projects.map((project) => {
    const label = project.displayName || project.name;
    return { value: project.name, label: (labels.get(label) ?? 0) > 1 ? `${label} (${project.name})` : label };
  });
}

export function useProjects() {
  const http = useHttp();
  return useQuery({
    queryKey: ["projects"],
    queryFn: async ({ signal }) => (await http.request<{ projects: Project[] }>("GET", "/proxy/v1/projects", { signal })).projects,
    staleTime: 30_000,
  });
}
