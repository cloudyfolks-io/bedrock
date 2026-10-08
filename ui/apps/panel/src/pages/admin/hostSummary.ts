import type { KubeObject } from "../../api/types";
import { listAt, textAt, valueAt } from "../../resources/path";

export function hostSummary(host: KubeObject): { cpu: string; memory: number; roles: string[] } {
  const cores = textAt(host, "status.inventory.cpu.cores");
  const threads = textAt(host, "status.inventory.cpu.threads");
  return {
    cpu: cores === "" ? "" : `${cores} / ${threads}`,
    memory: Number(valueAt(host, "status.inventory.memoryBytes") ?? 0),
    roles: listAt(host, "spec.roles").map(String),
  };
}
