import type { KubeObject } from "../../api/types";

export interface RestoreChoice {
  backupID: string;
  replace: boolean;
  suffix: string;
}

export function restoreBody(backup: KubeObject, choice: RestoreChoice): KubeObject {
  return {
    apiVersion: "bedrock.cloudyfolks.io/v1alpha1",
    kind: "VirtualMachineRestore",
    metadata: { name: `${backup.metadata.name}-${choice.suffix}`, ...(backup.metadata.namespace === undefined ? {} : { namespace: backup.metadata.namespace }) },
    spec: { backupRef: backup.metadata.name, backupID: choice.backupID, targetName: String(backup.spec?.vmRef ?? ""), replace: choice.replace },
  };
}
