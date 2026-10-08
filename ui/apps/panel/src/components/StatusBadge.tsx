import { msg } from "@lingui/core/macro";
import type { MessageDescriptor } from "@lingui/core";
import { useLingui } from "@lingui/react/macro";
import { Badge } from "@bedrock/design";
import { conditionOf } from "../api/kube";
import type { KubeObject } from "../api/types";
import { textAt } from "../resources/path";

type Tone = "neutral" | "success" | "warning" | "danger" | "info";

const phaseTones: Record<string, Tone> = {
  Running: "success",
  Ready: "success",
  Available: "success",
  InUse: "success",
  Completed: "success",
  Idle: "neutral",
  Stopped: "neutral",
  Pending: "warning",
  Starting: "info",
  Stopping: "info",
  Migrating: "info",
  Resizing: "info",
  Importing: "info",
  InProgress: "info",
  WaitingForUpload: "warning",
  Preflight: "info",
  Backup: "info",
  Preload: "info",
  ControlPlane: "info",
  Components: "info",
  Workers: "info",
  Verify: "info",
  Failed: "danger",
};

export const phaseMessages: Record<string, MessageDescriptor> = {
  Running: msg({ id: "status.phase.running", message: "Running" }),
  Ready: msg({ id: "status.phase.ready", message: "Ready" }),
  Available: msg({ id: "status.phase.available", message: "Available" }),
  InUse: msg({ id: "status.phase.inUse", message: "In use" }),
  Completed: msg({ id: "status.phase.completed", message: "Completed" }),
  Idle: msg({ id: "status.phase.idle", message: "Idle" }),
  Stopped: msg({ id: "status.phase.stopped", message: "Stopped" }),
  Pending: msg({ id: "status.phase.pending", message: "Pending" }),
  Starting: msg({ id: "status.phase.starting", message: "Starting" }),
  Stopping: msg({ id: "status.phase.stopping", message: "Stopping" }),
  Migrating: msg({ id: "status.phase.migrating", message: "Migrating" }),
  Resizing: msg({ id: "status.phase.resizing", message: "Resizing" }),
  Importing: msg({ id: "status.phase.importing", message: "Importing" }),
  InProgress: msg({ id: "status.phase.inProgress", message: "In progress" }),
  WaitingForUpload: msg({ id: "status.phase.waitingForUpload", message: "Waiting for upload" }),
  Preflight: msg({ id: "status.phase.preflight", message: "Preflight" }),
  Backup: msg({ id: "status.phase.backup", message: "Backup" }),
  Preload: msg({ id: "status.phase.preload", message: "Preload" }),
  ControlPlane: msg({ id: "status.phase.controlPlane", message: "Control plane" }),
  Components: msg({ id: "status.phase.components", message: "Components" }),
  Workers: msg({ id: "status.phase.workers", message: "Workers" }),
  Verify: msg({ id: "status.phase.verify", message: "Verify" }),
  Failed: msg({ id: "status.phase.failed", message: "Failed" }),
};

const conditionMessages = {
  True: msg({ id: "status.condition.ready", message: "Ready" }),
  False: msg({ id: "status.condition.notReady", message: "Not ready" }),
  Unknown: msg({ id: "status.condition.unknown", message: "Unknown" }),
  None: msg({ id: "status.condition.none", message: "No status" }),
};

const conditionTones: Record<keyof typeof conditionMessages, Tone> = {
  True: "success",
  False: "danger",
  Unknown: "warning",
  None: "neutral",
};

export function PhaseBadge({ phase }: { phase: string }) {
  const { i18n } = useLingui();
  const descriptor = phaseMessages[phase];
  return <Badge tone={phaseTones[phase] ?? "neutral"}>{descriptor ? i18n._(descriptor) : phase}</Badge>;
}

export function StatusBadge({ obj }: { obj: KubeObject }) {
  const { i18n } = useLingui();
  const phase = textAt(obj, "status.phase");
  if (phase !== "") {
    return <PhaseBadge phase={phase} />;
  }
  const ready = conditionOf(obj, "Ready")?.status ?? "None";
  return <Badge tone={conditionTones[ready]}>{i18n._(conditionMessages[ready])}</Badge>;
}
