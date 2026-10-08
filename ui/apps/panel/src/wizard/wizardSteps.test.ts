import { describe, expect, it } from "vitest";
import type { DiskValues, IsoValues } from "./types";
import { diskStepProblems, diskSteps, isoStepProblems, isoSteps, nextStep, previousStep, stepOfField, wizardReducer } from "./wizardSteps";

const environment = {
  vmNames: ["taken"],
  images: [{ name: "ubuntu", scope: "cluster" as const, type: "disk", phase: "Ready", sizeGi: 10, preference: "ubuntu" }],
  subnets: ["default"],
};

const values: DiskValues = {
  name: "web-1",
  imageName: "ubuntu",
  imageScope: "cluster",
  instanceType: "u1.small",
  preference: "ubuntu",
  rootDiskGi: 20,
  storageClass: "",
  extraVolumes: [],
  subnetRef: "default",
  securityGroups: [],
  sshKeys: [],
  userData: "",
  running: true,
};

describe("steps", () => {
  it("lists the steps of both wizards", () => {
    expect(diskSteps).toEqual(["basics", "image", "size", "disks", "network", "access", "review"]);
    expect(isoSteps).toEqual(["basics", "iso", "size", "firmware", "disks", "network", "review"]);
  });

  it("clamps at both ends", () => {
    expect(nextStep(diskSteps, "review")).toBe("review");
    expect(previousStep(diskSteps, "basics")).toBe("basics");
    expect(nextStep(diskSteps, "basics")).toBe("image");
    expect(wizardReducer({ index: 6 }, { type: "next" }, 7)).toEqual({ index: 6 });
    expect(wizardReducer({ index: 0 }, { type: "back" }, 7)).toEqual({ index: 0 });
    expect(wizardReducer({ index: 2 }, { type: "goto", index: 99 }, 7)).toEqual({ index: 6 });
  });

  it("a disk step is complete only when its fields validate", () => {
    expect(diskStepProblems("basics", values, environment)).toEqual([]);
    expect(diskStepProblems("basics", { ...values, name: "Bad_Name" }, environment)).toHaveLength(1);
    expect(diskStepProblems("basics", { ...values, name: "taken" }, environment)[0]?.field).toBe("name");
    expect(diskStepProblems("image", { ...values, imageName: "" }, environment)).toHaveLength(1);
    expect(diskStepProblems("size", { ...values, instanceType: "" }, environment)).toHaveLength(1);
    expect(diskStepProblems("disks", { ...values, rootDiskGi: 5 }, environment)).toHaveLength(1);
    expect(diskStepProblems("disks", { ...values, rootDiskGi: null }, environment)).toHaveLength(1);
    expect(diskStepProblems("network", { ...values, subnetRef: "" }, environment)).toHaveLength(1);
    expect(diskStepProblems("access", { ...values, userData: "hello" }, environment)).toHaveLength(1);
    expect(diskStepProblems("access", { ...values, userData: "#!/bin/sh" }, environment)).toEqual([]);
    expect(diskStepProblems("access", { ...values, userData: "#cloud-config\n" }, environment)).toEqual([]);
  });

  it("an ISO step is complete only when its fields validate", () => {
    const iso: IsoValues = { name: "win", isoName: "iso", extraCdroms: [], osPreference: "", instanceType: "u1.large", rootDiskGi: 60, storageClass: "", firmware: { uefi: true, secureBoot: false, tpm: false }, subnetRef: "default", securityGroups: [], running: false };
    expect(isoStepProblems("iso", { ...iso, isoName: "" }, environment)).toHaveLength(1);
    expect(isoStepProblems("disks", { ...iso, rootDiskGi: 0 }, environment)).toHaveLength(1);
    expect(isoStepProblems("firmware", iso, environment)).toEqual([]);
  });

  it("maps server fields to steps", () => {
    expect(stepOfField("spec.rootDisk.sizeGi")).toBe("disks");
    expect(stepOfField("metadata.name")).toBe("basics");
    expect(stepOfField("spec.networks[0].subnetRef")).toBe("network");
    expect(stepOfField("spec.unknown")).toBe("review");
  });
});
