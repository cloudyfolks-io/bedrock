import type { KubeList } from "../api/types";
import vms from "../fixtures/virtualmachines.json";

export function virtualMachines(): KubeList {
  return structuredClone(vms) as unknown as KubeList;
}
