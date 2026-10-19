import { describe, expect, it } from "vitest";
import { buildDiskVirtualMachine, buildIsoVirtualMachine } from "./buildVirtualMachine";
import type { DiskValues, IsoValues } from "./types";

const context = { namespace: "web-ns" };

const disk: DiskValues = {
  name: "web-1",
  imageName: "ubuntu-24.04",
  imageScope: "cluster",
  instanceType: "u1.medium",
  customSize: false,
  cores: null,
  memoryMiB: null,
  preference: "ubuntu",
  rootDiskGi: 40,
  storageClass: "block",
  extraVolumes: ["data-1"],
  subnetRef: "default",
  securityGroups: ["web"],
  sshKeys: ["laptop"],
  userData: "#cloud-config\n",
  running: true,
};

describe("buildDiskVirtualMachine", () => {
  it("builds the exact object", () => {
    expect(buildDiskVirtualMachine(disk, context)).toEqual({
      apiVersion: "bedrock.cloudyfolks.io/v1alpha1",
      kind: "VirtualMachine",
      metadata: { name: "web-1", namespace: "web-ns" },
      spec: {
        instanceType: "u1.medium",
        preference: "ubuntu",
        image: { name: "ubuntu-24.04", scope: "cluster" },
        rootDisk: { sizeGi: 40, storageClass: "block" },
        disks: [{ volumeRef: "data-1", bus: "virtio" }],
        networks: [{ subnetRef: "default", securityGroups: ["web"] }],
        cloudInit: { userData: "#cloud-config\n" },
        sshKeys: ["laptop"],
        running: true,
      },
    });
  });

  it("sends a custom size instead of the instance type", () => {
    const spec = buildDiskVirtualMachine({ ...disk, customSize: true, cores: 6, memoryMiB: 6144 }, context).spec as Record<string, unknown>;
    expect(spec.compute).toEqual({ cores: 6, memoryMiB: 6144 });
    expect(spec.instanceType).toBeUndefined();
  });

  it("omits empty optional parts", () => {
    const spec = buildDiskVirtualMachine({ ...disk, preference: "", storageClass: "", extraVolumes: [], securityGroups: [], sshKeys: [], userData: "" }, context).spec as Record<string, unknown>;
    expect(spec).toEqual({
      instanceType: "u1.medium",
      image: { name: "ubuntu-24.04", scope: "cluster" },
      rootDisk: { sizeGi: 40 },
      networks: [{ subnetRef: "default" }],
      running: true,
    });
  });

  it("never mutates its input", () => {
    const frozen = Object.freeze({ ...disk, extraVolumes: Object.freeze([...disk.extraVolumes]) as string[], securityGroups: Object.freeze([...disk.securityGroups]) as string[], sshKeys: Object.freeze([...disk.sshKeys]) as string[] });
    expect(() => buildDiskVirtualMachine(frozen, context)).not.toThrow();
  });
});

describe("buildIsoVirtualMachine", () => {
  const iso: IsoValues = {
    name: "win-1",
    isoName: "ubuntu-24.04-iso",
    extraCdroms: ["virtio-win"],
    osPreference: "windows.2k22",
    instanceType: "u1.large",
    customSize: false,
    cores: null,
    memoryMiB: null,
    rootDiskGi: 80,
    storageClass: "",
    firmware: { uefi: true, secureBoot: false, tpm: true },
    subnetRef: "default",
    securityGroups: [],
    running: false,
  };

  it("builds cdroms and firmware", () => {
    expect(buildIsoVirtualMachine(iso, context)).toEqual({
      apiVersion: "bedrock.cloudyfolks.io/v1alpha1",
      kind: "VirtualMachine",
      metadata: { name: "win-1", namespace: "web-ns" },
      spec: {
        instanceType: "u1.large",
        preference: "windows.2k22",
        cdroms: [{ imageRef: "ubuntu-24.04-iso" }, { imageRef: "virtio-win" }],
        firmware: { uefi: true, secureBoot: false, tpm: true },
        rootDisk: { sizeGi: 80 },
        networks: [{ subnetRef: "default" }],
        running: false,
      },
    });
    expect((buildIsoVirtualMachine(iso, context).spec as Record<string, unknown>).image).toBeUndefined();
  });
});
