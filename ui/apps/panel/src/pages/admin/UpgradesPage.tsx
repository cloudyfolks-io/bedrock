import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { msg } from "@lingui/core/macro";
import type { MessageDescriptor } from "@lingui/core";
import { Badge, Button, Select, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { conditionOf, kubeClient, listKey } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { ConfirmSimpleDialog, ConfirmTypedDialog } from "../../components/ConfirmDialogs";
import { EmptyState } from "../../components/EmptyState";
import { Ltr } from "../../components/Ltr";
import { PageHeader } from "../../components/PageHeader";
import { QueryFailure } from "../../components/QueryFailure";
import { PhaseBadge, phaseMessages } from "../../components/StatusBadge";
import { Spinner } from "../../components/Spinner";
import { MetricChart } from "../../charts/MetricChart";
import { formatDateTime } from "../../format";
import { LazyEditDialog } from "../../forms/LazyDialogs";
import { useLocale } from "../../LocaleProvider";
import { listAt, textAt } from "../../resources/path";
import { definitionFor, refOf, type ResourceDef } from "../../resources/registry";
import { useLiveList } from "../../stream/live";
import { phaseSteps, upgradeTargets } from "./upgradeHelpers";

const stateLabels: Record<string, MessageDescriptor> = {
  done: msg({ id: "upgrades.step.done", message: "Done" }),
  active: msg({ id: "upgrades.step.active", message: "In progress" }),
  pending: msg({ id: "upgrades.step.pending", message: "Pending" }),
  failed: msg({ id: "upgrades.step.failed", message: "Failed" }),
};

const conditionTypes = ["Available", "Progressing", "Degraded", "UpgradeBlocked"];
const rollbackPhases = ["Preflight", "Backup", "Preload", "ControlPlane"];

interface Component { name: string; version?: string; available: boolean; degraded: boolean; message?: string }
interface Backup { time: string; location: string; digest: string }

function requireDef(plural: string): ResourceDef {
  const def = definitionFor(plural);
  if (!def) {
    throw new Error(`missing definition ${plural}`);
  }
  return def;
}

export default function UpgradesPage() {
  const { t, i18n } = useLingui();
  const { locale } = useLocale();
  const http = useHttp();
  const toast = useToast();
  const queryClient = useQueryClient();
  const clusterDef = requireDef("clusters");
  const releaseDef = requireDef("releases");
  const clusters = useLiveList(refOf(clusterDef), {});
  const releases = useLiveList(refOf(releaseDef), {});
  const cluster: KubeObject | undefined = clusters.data?.items.find((item) => item.metadata.name === "cluster");
  const [target, setTarget] = useState("");
  const [confirmation, setConfirmation] = useState<{ action: "start" | "abort"; revision: string } | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [editing, setEditing] = useState(false);
  const [canPatch, canUpdate] = useCans([
    { verb: "patch", group: clusterDef.group, resource: clusterDef.plural, name: "cluster" },
    { verb: "update", group: clusterDef.group, resource: clusterDef.plural, name: "cluster" },
  ]);

  async function patch(spec: Record<string, unknown>) {
    const resourceVersion = cluster?.metadata.resourceVersion;
    if (!resourceVersion || submitting) {
      return;
    }
    setSubmitting(true);
    try {
      await kubeClient(http).mergePatch(refOf(clusterDef), undefined, "cluster", { metadata: { resourceVersion }, spec });
    } catch (error) {
      toast.show({ tone: "danger", title: t({ id: "action.error.failed", message: "The action failed" }), description: error instanceof Error ? error.message : "" });
    } finally {
      await queryClient.invalidateQueries({ queryKey: listKey(refOf(clusterDef), undefined, {}) });
      setSubmitting(false);
    }
  }

  if (clusters.isPending || releases.isPending) {
    return <Spinner />;
  }
  if (clusters.isError || releases.isError) {
    return (
      <>
        <PageHeader title={t({ id: "nav.item.upgrades", message: "Upgrades" })} />
        <QueryFailure error={clusters.error ?? releases.error} retry={() => void Promise.all([clusters.refetch(), releases.refetch()])} />
      </>
    );
  }
  if (!cluster) {
    return <EmptyState title={t({ id: "list.empty.title", message: "Nothing here yet" })} />;
  }
  const phase = textAt(cluster, "status.phase");
  const current = textAt(cluster, "status.version");
  const desired = textAt(cluster, "spec.desiredVersion");
  const action = textAt(cluster, "spec.upgrade.action");
  const activeTarget = textAt(cluster, "status.upgrade.to");
  const failedPhase = textAt(cluster, "status.upgrade.failedPhase");
  const message = textAt(cluster, "status.upgrade.message");
  const startedAt = textAt(cluster, "status.upgrade.startedAt");
  const phaseStartedAt = textAt(cluster, "status.upgrade.phaseStartedAt");
  const steps = phaseSteps(phase, failedPhase);
  const targets = upgradeTargets(releases.data?.items ?? [], current);
  const targetAvailable = targets.some((release) => textAt(release, "spec.version") === target);
  const progressing = conditionOf(cluster, "Progressing");
  const blocked = conditionOf(cluster, "UpgradeBlocked");
  const blockedRequest = blocked?.status === "True" && blocked.observedGeneration === cluster.metadata.generation;
  const upgradeRequested = phase === "Idle" && desired !== "" && desired !== current && !blockedRequest;
  const revision = cluster.metadata.resourceVersion ?? "";
  const writable = canPatch === "allowed" && revision !== "";
  const canStart = writable && phase === "Idle" && activeTarget === "" && action === "" && !upgradeRequested;
  const failed = writable && phase === "Failed" && activeTarget !== "" && action === "" && progressing?.reason !== "RestoreManual";
  const canResume = failed && failedPhase !== "";
  const canAbort = failed && rollbackPhases.includes(failedPhase);
  const confirmStart = canStart && targetAvailable && confirmation?.action === "start" && confirmation.revision === revision && !submitting;
  const confirmAbort = canAbort && confirmation?.action === "abort" && confirmation.revision === revision && !submitting;
  const requestMessage = action === "abort" ? t({ id: "upgrades.rollbackRequested", message: "Rollback requested." })
    : action === "resume" ? t({ id: "upgrades.resumeRequested", message: "Resume requested." })
    : upgradeRequested ? t({ id: "upgrades.startRequested", message: "Upgrade requested." })
    : "";

  return (
    <>
      <PageHeader
        title={t({ id: "nav.item.upgrades", message: "Upgrades" })}
        actions={
          canUpdate === "allowed" ? (
            <Button variant="secondary" onClick={() => setEditing(true)}>
              {t({ id: "upgrades.editSettings", message: "Edit upgrade settings" })}
            </Button>
          ) : null
        }
      />
      <dl className="br-facts">
        <dt>{t({ id: "col.version", message: "Version" })}</dt>
        <dd><Ltr>{current || "—"}</Ltr></dd>
        <dt>{t({ id: "col.desiredVersion", message: "Desired version" })}</dt>
        <dd><Ltr>{desired}</Ltr></dd>
        <dt>{t({ id: "col.phase", message: "Phase" })}</dt>
        <dd>{phase ? <PhaseBadge phase={phase} /> : "—"}</dd>
        <dt>{t({ id: "upgrades.window", message: "Maintenance window" })}</dt>
        <dd>{textAt(cluster, "spec.maintenanceWindow") || "—"}</dd>
        <dt>{t({ id: "upgrades.concurrency", message: "Node concurrency" })}</dt>
        <dd>{textAt(cluster, "spec.nodeConcurrency") || "—"}</dd>
        {startedAt ? (
          <>
            <dt>{t({ id: "upgrades.startedAt", message: "Upgrade start (UTC)" })}</dt>
            <dd><time dateTime={startedAt}>{formatDateTime(startedAt, locale)}</time></dd>
          </>
        ) : null}
        {phaseStartedAt ? (
          <>
            <dt>{t({ id: "upgrades.phaseStartedAt", message: "Phase start (UTC)" })}</dt>
            <dd><time dateTime={phaseStartedAt}>{formatDateTime(phaseStartedAt, locale)}</time></dd>
          </>
        ) : null}
      </dl>
      <ol className="br-steps" aria-label={t({ id: "upgrades.timeline", message: "Upgrade phases" })}>
        {steps.map((step) => (
          <li key={step.name} className="br-step" data-state={step.state} aria-current={step.state === "active" ? "step" : undefined}>
            <span>{phaseMessages[step.name] ? i18n._(phaseMessages[step.name] as MessageDescriptor) : step.name}</span>{" "}
            <Badge tone={step.state === "done" ? "success" : step.state === "failed" ? "danger" : step.state === "active" ? "info" : "neutral"}>{i18n._(stateLabels[step.state] as MessageDescriptor)}</Badge>
          </li>
        ))}
      </ol>
      {requestMessage ? <p role="status" className="br-help">{requestMessage}</p> : null}
      {message ? <p role="status" className="br-help">{message}</p> : null}
      {blockedRequest ? <p role="status" className="br-help">{blocked.message ?? blocked.reason}</p> : null}
      {canStart ? (
        <section className="br-section">
          <h2>{t({ id: "upgrades.start.title", message: "Start an upgrade" })}</h2>
          <Select
            label={t({ id: "upgrades.target", message: "Target version" })}
            value={target}
            options={targets.map((release) => ({ value: textAt(release, "spec.version"), label: textAt(release, "spec.version") }))}
            onValueChange={setTarget}
          />
          <Button variant="primary" disabled={!targetAvailable || submitting} onClick={() => setConfirmation({ action: "start", revision })}>
            {t({ id: "upgrades.start.button", message: "Start upgrade" })}
          </Button>
        </section>
      ) : null}
      {canResume || canAbort ? (
        <section className="br-section">
          <h2>{t({ id: "upgrades.recovery", message: "Upgrade recovery" })}</h2>
          {canResume ? (
            <Button variant="secondary" disabled={submitting} onClick={() => void patch({ upgrade: { action: "resume" } })}>
              {t({ id: "upgrades.resume", message: "Resume" })}
            </Button>
          ) : null}
          {canAbort ? (
            <Button variant="danger" disabled={submitting} onClick={() => setConfirmation({ action: "abort", revision })}>
              {t({ id: "upgrades.abort", message: "Abort" })}
            </Button>
          ) : null}
        </section>
      ) : null}
      <Table caption={t({ id: "detail.conditions.title", message: "Conditions" })}>
        <TableHead>
          <TableRow>
            <TableHeaderCell>{t({ id: "detail.conditions.type", message: "Type" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "detail.conditions.status", message: "Status" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "detail.conditions.reason", message: "Reason" })}</TableHeaderCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {conditionTypes.map((type) => {
            const condition = conditionOf(cluster, type);
            return (
              <TableRow key={type}>
                <TableCell><Ltr>{type}</Ltr></TableCell>
                <TableCell>{condition?.status ?? "—"}</TableCell>
                <TableCell>{condition?.reason ? <Ltr>{condition.reason}</Ltr> : "—"}</TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
      <Table caption={t({ id: "upgrades.components", message: "Components" })}>
        <TableHead>
          <TableRow>
            <TableHeaderCell>{t({ id: "col.name", message: "Name" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "col.version", message: "Version" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "upgrades.available", message: "Available" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "upgrades.degraded", message: "Degraded" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "detail.conditions.message", message: "Message" })}</TableHeaderCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {(listAt(cluster, "status.components") as Component[]).map((component) => (
            <TableRow key={component.name}>
              <TableCell><Ltr>{component.name}</Ltr></TableCell>
              <TableCell><Ltr>{component.version ?? ""}</Ltr></TableCell>
              <TableCell>{component.available ? t({ id: "common.yes", message: "Yes" }) : t({ id: "common.no", message: "No" })}</TableCell>
              <TableCell>{component.degraded ? t({ id: "common.yes", message: "Yes" }) : t({ id: "common.no", message: "No" })}</TableCell>
              <TableCell>{component.message ?? ""}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <Table caption={t({ id: "upgrades.backups", message: "Upgrade backups" })}>
        <TableHead>
          <TableRow>
            <TableHeaderCell>{t({ id: "upgrades.backup.time", message: "Time" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "upgrades.backup.location", message: "Location" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "col.digest", message: "Digest" })}</TableHeaderCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {(listAt(cluster, "status.backups") as Backup[]).map((backup) => (
            <TableRow key={`${backup.time}-${backup.location}`}>
              <TableCell>{backup.time}</TableCell>
              <TableCell><Ltr>{backup.location}</Ltr></TableCell>
              <TableCell><Ltr>{backup.digest}</Ltr></TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <MetricChart title={t({ id: "upgrades.ceph", message: "Ceph usage" })} queries={["ceph.used", "ceph.capacity"]} unit="bytes" />
      <ConfirmSimpleDialog
        open={confirmStart}
        title={t({ id: "upgrades.start.title", message: "Start an upgrade" })}
        description={t({ id: "upgrades.start.description", message: `Upgrade the cluster to ${target}.` })}
        confirmLabel={t({ id: "upgrades.start.button", message: "Start upgrade" })}
        onConfirm={() => {
          setConfirmation(null);
          if (confirmStart) {
            void patch(blockedRequest && target === desired ? { upgrade: { action: "resume" } } : { desiredVersion: target });
          }
        }}
        onCancel={() => setConfirmation(null)}
      />
      <ConfirmTypedDialog
        open={confirmAbort}
        word="abort"
        title={t({ id: "upgrades.abort.title", message: "Abort the upgrade" })}
        description={t({ id: "upgrades.abort.description", message: "Type abort to stop the failed upgrade." })}
        confirmLabel={t({ id: "upgrades.abort", message: "Abort" })}
        onConfirm={() => {
          setConfirmation(null);
          if (confirmAbort) {
            void patch({ upgrade: { action: "abort" } });
          }
        }}
        onCancel={() => setConfirmation(null)}
      />
      {editing ? <LazyEditDialog def={clusterDef} object={cluster} onClose={() => setEditing(false)} /> : null}
    </>
  );
}
