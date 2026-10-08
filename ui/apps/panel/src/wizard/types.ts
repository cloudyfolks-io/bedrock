export type ImageScope = "cluster" | "project";

export interface DiskValues {
  name: string;
  imageName: string;
  imageScope: ImageScope;
  instanceType: string;
  preference: string;
  rootDiskGi: number | null;
  storageClass: string;
  extraVolumes: string[];
  subnetRef: string;
  securityGroups: string[];
  sshKeys: string[];
  userData: string;
  running: boolean;
}

export interface Firmware {
  uefi: boolean;
  secureBoot: boolean;
  tpm: boolean;
}

export interface IsoValues {
  name: string;
  isoName: string;
  extraCdroms: string[];
  osPreference: string;
  instanceType: string;
  rootDiskGi: number | null;
  storageClass: string;
  firmware: Firmware;
  subnetRef: string;
  securityGroups: string[];
  running: boolean;
}

export type DiskStep = "basics" | "image" | "size" | "disks" | "network" | "access" | "review";
export type IsoStep = "basics" | "iso" | "size" | "firmware" | "disks" | "network" | "review";
export type WizardStep = DiskStep | IsoStep;

export interface WizardImage {
  name: string;
  scope: ImageScope;
  type: string;
  phase: string;
  sizeGi: number;
  preference: string;
}

export interface WizardContext {
  namespace: string;
}
