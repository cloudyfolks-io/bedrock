import { msg } from "@lingui/core/macro";
import type { MessageDescriptor } from "@lingui/core";
import type { DiskStep, DiskValues, IsoStep, IsoValues, WizardImage } from "./types";

export const diskSteps: DiskStep[] = ["basics", "image", "size", "disks", "network", "access", "review"];
export const isoSteps: IsoStep[] = ["basics", "iso", "size", "firmware", "disks", "network", "review"];

export interface WizardState {
  index: number;
}

export type WizardAction = { type: "next" } | { type: "back" } | { type: "goto"; index: number };

export function wizardReducer(state: WizardState, action: WizardAction, total: number): WizardState {
  const clamp = (index: number) => Math.max(0, Math.min(total - 1, index));
  switch (action.type) {
    case "next":
      return { index: clamp(state.index + 1) };
    case "back":
      return { index: clamp(state.index - 1) };
    case "goto":
      return { index: clamp(action.index) };
  }
}

export function nextStep<T>(steps: readonly T[], current: T): T {
  const index = steps.indexOf(current);
  return steps[Math.min(steps.length - 1, index + 1)] ?? current;
}

export function previousStep<T>(steps: readonly T[], current: T): T {
  const index = steps.indexOf(current);
  return steps[Math.max(0, index - 1)] ?? current;
}

export interface Problem {
  field: string;
  message: MessageDescriptor;
}

export interface StepEnvironment {
  vmNames: readonly string[];
  images: readonly WizardImage[];
  subnets: readonly string[];
}

const dnsLabel = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

const messages = {
  badName: msg({ id: "wizard.error.name", message: "Use lowercase letters, digits and hyphens. Start and end with a letter or digit." }),
  duplicate: msg({ id: "wizard.error.duplicate", message: "A virtual machine with this name already exists" }),
  chooseImage: msg({ id: "wizard.error.image", message: "Choose an image" }),
  chooseIso: msg({ id: "wizard.error.iso", message: "Choose an ISO image" }),
  chooseSize: msg({ id: "wizard.error.size", message: "Choose an instance type" }),
  cores: msg({ id: "wizard.error.cores", message: "Enter the number of CPU cores" }),
  memory: msg({ id: "wizard.error.memory", message: "Enter at least 128 MiB of memory" }),
  rootDisk: msg({ id: "wizard.error.rootDisk", message: "Enter a root disk size in GiB" }),
  rootDiskSmall: msg({ id: "wizard.error.rootDiskSmall", message: "The root disk is smaller than the image" }),
  subnet: msg({ id: "wizard.error.subnet", message: "Choose a subnet" }),
  userData: msg({ id: "wizard.error.userData", message: "Cloud-init data must start with #cloud-config or #!" }),
};

function nameProblems(name: string, vmNames: readonly string[]): Problem[] {
  if (!dnsLabel.test(name) || name.length > 63) {
    return [{ field: "name", message: messages.badName }];
  }
  return vmNames.includes(name) ? [{ field: "name", message: messages.duplicate }] : [];
}

function rootDiskProblems(sizeGi: number | null, minimum: number): Problem[] {
  if (sizeGi === null || sizeGi < 1) {
    return [{ field: "rootDiskGi", message: messages.rootDisk }];
  }
  return sizeGi < minimum ? [{ field: "rootDiskGi", message: messages.rootDiskSmall }] : [];
}

function sizeProblems(values: { instanceType: string; customSize: boolean; cores: number | null; memoryMiB: number | null }): Problem[] {
  if (!values.customSize) {
    return values.instanceType === "" ? [{ field: "instanceType", message: messages.chooseSize }] : [];
  }
  const problems: Problem[] = [];
  if (values.cores === null || !Number.isInteger(values.cores) || values.cores < 1) {
    problems.push({ field: "cores", message: messages.cores });
  }
  if (values.memoryMiB === null || !Number.isInteger(values.memoryMiB) || values.memoryMiB < 128) {
    problems.push({ field: "memoryMiB", message: messages.memory });
  }
  return problems;
}

function subnetProblems(subnetRef: string): Problem[] {
  return subnetRef === "" ? [{ field: "subnetRef", message: messages.subnet }] : [];
}

export function diskStepProblems(step: DiskStep, values: DiskValues, environment: StepEnvironment): Problem[] {
  switch (step) {
    case "basics":
      return nameProblems(values.name, environment.vmNames);
    case "image":
      return values.imageName === "" ? [{ field: "imageName", message: messages.chooseImage }] : [];
    case "size":
      return sizeProblems(values);
    case "disks": {
      const image = environment.images.find((candidate) => candidate.name === values.imageName && candidate.scope === values.imageScope);
      return rootDiskProblems(values.rootDiskGi, image?.sizeGi ?? 1);
    }
    case "network":
      return subnetProblems(values.subnetRef);
    case "access":
      return values.userData === "" || values.userData.startsWith("#cloud-config") || values.userData.startsWith("#!") ? [] : [{ field: "userData", message: messages.userData }];
    case "review":
      return [];
  }
}

export function isoStepProblems(step: IsoStep, values: IsoValues, environment: StepEnvironment): Problem[] {
  switch (step) {
    case "basics":
      return nameProblems(values.name, environment.vmNames);
    case "iso":
      return values.isoName === "" ? [{ field: "isoName", message: messages.chooseIso }] : [];
    case "size":
      return sizeProblems(values);
    case "disks":
      return rootDiskProblems(values.rootDiskGi, 1);
    case "network":
      return subnetProblems(values.subnetRef);
    case "firmware":
    case "review":
      return [];
  }
}

export function stepOfField(field: string): string {
  if (field === "metadata.name" || field === "spec.name") {
    return "basics";
  }
  const table: [string, string][] = [
    ["spec.image", "image"],
    ["spec.cdroms", "iso"],
    ["spec.instanceType", "size"],
    ["spec.compute", "size"],
    ["spec.preference", "size"],
    ["spec.rootDisk", "disks"],
    ["spec.disks", "disks"],
    ["spec.firmware", "firmware"],
    ["spec.networks", "network"],
    ["spec.cloudInit", "access"],
    ["spec.sshKeys", "access"],
  ];
  return table.find(([prefix]) => field.startsWith(prefix))?.[1] ?? "review";
}

export function hasChanges<T>(values: T, initial: T): boolean {
  return JSON.stringify(values) !== JSON.stringify(initial);
}
