import type { KubeObject } from "../api/types";
import { textAt } from "./path";

export type VmAction = "start" | "stop" | "restart" | "migrate" | "snapshot" | "delete";
export type ActionState = "enabled" | "disabled";

const enabledByPhase: Record<string, VmAction[]> = {
  Stopped: ["start", "snapshot", "delete"],
  Running: ["stop", "restart", "migrate", "snapshot", "delete"],
  Starting: ["delete"],
  Stopping: ["delete"],
  Migrating: [],
  Failed: ["start", "delete"],
};

const actions: VmAction[] = ["start", "stop", "restart", "migrate", "snapshot", "delete"];

export function actionStates(vm: KubeObject): Record<VmAction, ActionState> {
  const enabled = enabledByPhase[textAt(vm, "status.phase")] ?? [];
  return Object.fromEntries(actions.map((action) => [action, enabled.includes(action) ? "enabled" : "disabled"])) as Record<VmAction, ActionState>;
}
