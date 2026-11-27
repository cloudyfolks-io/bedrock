import type { KubeObject } from "../api/types";
import { textAt } from "./path";

export function vmSize(object: KubeObject): string {
  const instanceType = textAt(object, "spec.instanceType");
  if (instanceType !== "") {
    return instanceType;
  }
  const cores = textAt(object, "spec.compute.cores");
  return cores === "" ? "" : `${cores} vCPU · ${textAt(object, "spec.compute.memoryMiB")} MiB`;
}
