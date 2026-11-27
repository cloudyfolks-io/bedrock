import { useQueries } from "@tanstack/react-query";
import { useHttp } from "../api/HttpProvider";
import { kubeClient } from "../api/kube";
import type { KubeObject } from "../api/types";
import { definitionFor, refOf, type ResourceDef } from "../resources/registry";
import { loadBalancerElasticIPOptions } from "../resources/summaries";

export function useRefOptions(def: ResourceDef, namespace: string | undefined, tenant: string | undefined, name = "", currentElasticIP = ""): Record<string, string[]> {
  const http = useHttp();
  const entries = Object.entries(def.refFields ?? {});
  const results = useQueries({
    queries: entries.map(([path, plural]) => {
      const target = definitionFor(plural);
      return {
        queryKey: ["ref-options", plural, target?.scope === "Namespaced" ? (namespace ?? "") : "", tenant ?? ""],
        enabled: target !== undefined && (target.scope !== "Namespaced" || namespace !== undefined),
        retry: false,
        staleTime: 30_000,
        queryFn: async () => {
          if (!target) {
            return [] as KubeObject[];
          }
          const list = await kubeClient(http).list(refOf(target), target.scope === "Namespaced" && namespace !== undefined ? { namespace } : {});
          return list.items
            .filter((item) => target.plural !== "networks" || tenant === undefined || item.spec?.tenantRef === tenant);
        },
      };
    }),
  });
  return Object.fromEntries(entries.map(([path], index) => {
    const result = results[index];
    const items = result?.isSuccess ? result.data : [];
    return [path, def.plural === "loadbalancers" && path === "elasticIPRef"
      ? loadBalancerElasticIPOptions(items.filter((item) => item.metadata.namespace === namespace), name, currentElasticIP)
      : items.map((item) => item.metadata.name)];
  }));
}
