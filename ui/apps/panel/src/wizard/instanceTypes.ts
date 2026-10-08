export interface InstanceType {
  name: string;
  vcpu: number;
  memoryGiB: number;
}

export const instanceTypes: InstanceType[] = [
  { name: "u1.nano", vcpu: 1, memoryGiB: 0.5 },
  { name: "u1.micro", vcpu: 1, memoryGiB: 1 },
  { name: "u1.small", vcpu: 1, memoryGiB: 2 },
  { name: "u1.medium", vcpu: 1, memoryGiB: 4 },
  { name: "u1.large", vcpu: 2, memoryGiB: 8 },
  { name: "u1.xlarge", vcpu: 4, memoryGiB: 16 },
  { name: "u1.2xlarge", vcpu: 8, memoryGiB: 32 },
  { name: "u1.4xlarge", vcpu: 16, memoryGiB: 64 },
  { name: "u1.8xlarge", vcpu: 32, memoryGiB: 128 },
];

export const windowsPreferences = ["windows.2k22", "windows.11"];
