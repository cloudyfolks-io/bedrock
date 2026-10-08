import { useQueries } from "@tanstack/react-query";
import { useHttp } from "../api/HttpProvider";
import { isHttpError } from "../api/http";
import { kubeClient, type ResourceRef } from "../api/kube";
import type { KubeObject } from "../api/types";
import { textAt, valueAt } from "../resources/path";
import { definitionFor, refOf } from "../resources/registry";
import { useCurrentProject } from "../shell/context";
import { instanceTypes as fixedInstanceTypes, type InstanceType } from "./instanceTypes";
import type { WizardImage } from "./types";

export interface WizardData {
  ready: boolean;
  namespace: string;
  images: WizardImage[];
  vmNames: string[];
  subnets: string[];
  securityGroups: string[];
  sshKeys: string[];
  volumes: string[];
  instanceTypes: InstanceType[];
  defaultSubnet: string;
}

const instanceTypeRef: ResourceRef = { group: "instancetype.kubevirt.io", version: "v1beta1", plural: "virtualmachineclusterinstancetypes", namespaced: false };
const gib = 1024 ** 3;

function required(plural: string) {
  const def = definitionFor(plural);
  if (!def) {
    throw new Error(`missing definition ${plural}`);
  }
  return def;
}

function imageOf(object: KubeObject, scope: WizardImage["scope"]): WizardImage {
  const sizeGi = Number(valueAt(object, "spec.sizeGi") ?? 0) || Math.ceil(Number(valueAt(object, "status.sizeBytes") ?? 0) / gib);
  return {
    name: object.metadata.name,
    scope,
    type: textAt(object, "spec.type"),
    phase: textAt(object, "status.phase"),
    sizeGi,
    preference: textAt(object, "spec.os.preference"),
  };
}

function toInstanceType(object: KubeObject): InstanceType | undefined {
  const vcpu = Number(valueAt(object, "spec.cpu.guest"));
  const memory = textAt(object, "spec.memory.guest");
  const match = /^(\d+(?:\.\d+)?)(Mi|Gi)$/.exec(memory);
  if (!Number.isFinite(vcpu) || !match?.[1]) {
    return undefined;
  }
  return { name: object.metadata.name, vcpu, memoryGiB: match[2] === "Gi" ? Number(match[1]) : Number(match[1]) / 1024 };
}

export function useWizardData(): WizardData {
  const http = useHttp();
  const project = useCurrentProject();
  const namespace = project?.namespace ?? "";
  const client = kubeClient(http);
  const list = (plural: string) => ({
    queryKey: ["wizard", plural, namespace],
    enabled: namespace !== "",
    retry: false,
    staleTime: 5_000,
    queryFn: async () => (await client.list(refOf(required(plural)), required(plural).scope === "Namespaced" ? { namespace } : {})).items,
  });
  const [images, clusterImages, vms, subnets, groups, keys, volumes, types, projectObject] = useQueries({
    queries: [
      list("images"),
      { ...list("clusterimages"), enabled: namespace !== "" },
      list("virtualmachines"),
      list("subnets"),
      list("securitygroups"),
      list("sshkeys"),
      list("volumes"),
      {
        queryKey: ["wizard", "instancetypes"],
        retry: false,
        staleTime: 5 * 60_000,
        queryFn: async () => (await client.list(instanceTypeRef, {})).items.map(toInstanceType).filter((entry): entry is InstanceType => entry !== undefined),
      },
      {
        queryKey: ["wizard", "project", project?.name ?? ""],
        enabled: project !== undefined,
        retry: false,
        staleTime: 60_000,
        queryFn: () => client.get(refOf(required("projects")), undefined, project?.name ?? ""),
      },
    ],
  });
  const subnetNames = (subnets.data ?? []).map((item) => item.metadata.name);
  const projectSubnet = textAt(projectObject.data ?? { metadata: { name: "" } }, "status.subnet");
  const defaultSubnet = subnetNames.includes(projectSubnet) ? projectSubnet : subnetNames.includes("default") ? "default" : (subnetNames[0] ?? "");
  const loaded = [images, vms, subnets].every((query) => query.isSuccess || (query.isError && isHttpError(query.error)));
  const remoteTypes = types.data && types.data.length > 0 ? types.data : fixedInstanceTypes;
  return {
    ready: namespace !== "" && loaded,
    namespace,
    images: [...(images.data ?? []).map((item) => imageOf(item, "project")), ...(clusterImages.data ?? []).map((item) => imageOf(item, "cluster"))],
    vmNames: (vms.data ?? []).map((item) => item.metadata.name),
    subnets: subnetNames,
    securityGroups: (groups.data ?? []).map((item) => item.metadata.name),
    sshKeys: (keys.data ?? []).map((item) => item.metadata.name),
    volumes: (volumes.data ?? []).map((item) => item.metadata.name),
    instanceTypes: remoteTypes,
    defaultSubnet,
  };
}
