import { useQuery } from "@tanstack/react-query";
import { useHttp } from "./HttpProvider";

export interface Project {
  name: string;
  displayName: string;
  tenant: string;
  namespace: string;
}

export function useProjects() {
  const http = useHttp();
  return useQuery({
    queryKey: ["projects"],
    queryFn: async () => (await http.request<{ projects: Project[] }>("GET", "/proxy/v1/projects")).projects,
    staleTime: 30_000,
  });
}
