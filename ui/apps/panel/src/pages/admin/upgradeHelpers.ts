import type { KubeObject } from "../../api/types";
import { listAt, textAt } from "../../resources/path";

export const upgradePhases = ["Idle", "Preflight", "Backup", "Preload", "ControlPlane", "Components", "Workers", "Verify"] as const;

export interface PhaseStep {
  name: string;
  state: "done" | "active" | "pending" | "failed";
}

export function phaseSteps(phase: string, failedPhase = ""): PhaseStep[] {
  const failed = phase === "Failed";
  const current = upgradePhases.indexOf((failed ? failedPhase : phase) as (typeof upgradePhases)[number]);
  return upgradePhases.map((name, index) => ({
    name,
    state: current < 0 ? "pending" : index < current ? "done" : index === current ? (failed ? "failed" : "active") : "pending",
  }));
}

export function upgradeTargets(releases: readonly KubeObject[], current: string): KubeObject[] {
  return releases.filter((release) => textAt(release, "spec.version") !== current && listAt(release, "spec.upgradeFrom").includes(current));
}
