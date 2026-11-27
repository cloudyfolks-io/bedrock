import { definitionFor, refOf, type ResourceDef } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { useCurrentProject } from "../../shell/context";
import { useLiveList } from "../../stream/live";

function LoadBalancerList({ def, eips, namespace }: { def: ResourceDef; eips: ResourceDef; namespace: string }) {
  const list = useLiveList(refOf(eips), { namespace });
  return <ResourceListPage def={def} {...(list.isSuccess ? { elasticIps: list.data.items } : {})} />;
}

export default function LoadBalancersPage() {
  const def = definitionFor("loadbalancers");
  const eips = definitionFor("elasticips");
  const project = useCurrentProject();
  if (!def) {
    return null;
  }
  return eips && project?.namespace ? <LoadBalancerList def={def} eips={eips} namespace={project.namespace} /> : <ResourceListPage def={def} />;
}
