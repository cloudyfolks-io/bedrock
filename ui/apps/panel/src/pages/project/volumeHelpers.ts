import type { KubeObject } from "../../api/types";

export function canExpand(volume: KubeObject, newSizeGi: number): boolean {
  const current = Number(volume.spec?.sizeGi ?? 0);
  return Number.isInteger(newSizeGi) && newSizeGi > current;
}
