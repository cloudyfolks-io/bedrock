import type { KubeObject } from "../api/types";
import type { DiskValues, IsoValues, WizardContext } from "./types";

function omitEmpty(entries: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(entries).filter(([, value]) => value !== "" && value !== undefined && !(Array.isArray(value) && value.length === 0)));
}

function networks(subnetRef: string, securityGroups: readonly string[]) {
  return [omitEmpty({ subnetRef, securityGroups: [...securityGroups] })];
}

function rootDisk(sizeGi: number | null, storageClass: string) {
  return omitEmpty({ sizeGi: sizeGi ?? 0, storageClass });
}

function envelope(name: string, namespace: string, spec: Record<string, unknown>): KubeObject {
  return { apiVersion: "bedrock.cloudyfolks.io/v1alpha1", kind: "VirtualMachine", metadata: { name, namespace }, spec };
}

export function buildDiskVirtualMachine(values: DiskValues, context: WizardContext): KubeObject {
  return envelope(
    values.name,
    context.namespace,
    omitEmpty({
      instanceType: values.instanceType,
      preference: values.preference,
      image: { name: values.imageName, scope: values.imageScope },
      rootDisk: rootDisk(values.rootDiskGi, values.storageClass),
      disks: values.extraVolumes.map((volumeRef) => ({ volumeRef, bus: "virtio" })),
      networks: networks(values.subnetRef, values.securityGroups),
      cloudInit: values.userData === "" ? "" : { userData: values.userData },
      sshKeys: [...values.sshKeys],
      running: values.running,
    }),
  );
}

export function buildIsoVirtualMachine(values: IsoValues, context: WizardContext): KubeObject {
  return envelope(
    values.name,
    context.namespace,
    omitEmpty({
      instanceType: values.instanceType,
      preference: values.osPreference,
      cdroms: [values.isoName, ...values.extraCdroms].filter((name) => name !== "").map((imageRef) => ({ imageRef })),
      firmware: { ...values.firmware },
      rootDisk: rootDisk(values.rootDiskGi, values.storageClass),
      networks: networks(values.subnetRef, values.securityGroups),
      running: values.running,
    }),
  );
}
