import { useQuery } from "@tanstack/react-query";
import { useHttp } from "./HttpProvider";
import type { OpenApiDocument } from "../forms/schema";

interface OpenApiIndex {
  paths: Record<string, { serverRelativeURL: string }>;
}

export function useOpenAPIDocument(group: string, version: string) {
  const http = useHttp();
  return useQuery({
    queryKey: ["openapi", group, version],
    staleTime: Infinity,
    queryFn: async () => {
      const index = await http.request<OpenApiIndex>("GET", "/openapi/v3");
      const entry = index.paths[`apis/${group}/${version}`];
      if (!entry) {
        throw new Error(`no OpenAPI document for ${group}/${version}`);
      }
      return http.request<OpenApiDocument>("GET", entry.serverRelativeURL);
    },
  });
}
