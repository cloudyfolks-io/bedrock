import { useQueries } from "@tanstack/react-query";
import { useHttp } from "../api/HttpProvider";
import { kubeClient } from "../api/kube";
import { definitionFor, refOf, type ResourceDef } from "../resources/registry";

export function useRefOptions(def: ResourceDef, namespace: string | undefined, tenant: string | undefined): Record<string, string[]> {
  const http = useHttp();
  const entries = Object.entries(def.refFields ?? {});
  const results = useQueries({
    queries: entries.map(([path, plural]) => {
      const target = definitionFor(plural);
      return {
        queryKey: ["ref-options", plural, target?.scope === "Namespaced" ? (namespace ?? "") : "", tenant ?? ""],
        enabled: target !== undefined,
        retry: false,
        staleTime: 30_000,
        queryFn: async () => {
          if (!target) {
            return [] as string[];
          }
          const list = await kubeClient(http).list(refOf(target), target.scope === "Namespaced" && namespace !== undefined ? { namespace } : {});
          return list.items
            .filter((item) => target.scope === "Namespaced" || tenant === undefined || target.plural !== "networks" || item.spec?.tenantRef === tenant)
            .map((item) => item.metadata.name);
        },
      };
    }),
  });
  return Object.fromEntries(entries.map(([path], index) => [path, results[index]?.data ?? []]));
}
