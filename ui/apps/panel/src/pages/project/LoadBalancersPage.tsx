import { definitionFor, refOf } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { useCurrentProject } from "../../shell/context";
import { useLiveList } from "../../stream/live";

export default function LoadBalancersPage() {
  const def = definitionFor("loadbalancers");
  const eips = definitionFor("elasticips");
  const project = useCurrentProject();
  const list = useLiveList(eips ? refOf(eips) : { group: "", version: "", plural: "", namespaced: true }, project ? { namespace: project.namespace } : {});
  if (!def) {
    return null;
  }
  return <ResourceListPage def={def} {...(list.data ? { elasticIps: list.data.items } : {})} />;
}
