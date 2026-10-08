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
import { PhaseBadge, phaseMessages } from "../../components/StatusBadge";
import { Spinner } from "../../components/Spinner";
import { LazyEditDialog } from "../../forms/LazyDialogs";
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
  const http = useHttp();
  const toast = useToast();
  const queryClient = useQueryClient();
  const clusterDef = requireDef("clusters");
  const releaseDef = requireDef("releases");
  const clusters = useLiveList(refOf(clusterDef), {});
  const releases = useLiveList(refOf(releaseDef), {});
  const cluster: KubeObject | undefined = clusters.data?.items.find((item) => item.metadata.name === "cluster") ?? clusters.data?.items[0];
  const [target, setTarget] = useState("");
  const [starting, setStarting] = useState(false);
  const [aborting, setAborting] = useState(false);
  const [editing, setEditing] = useState(false);
  const [canUpdate] = useCans([{ verb: "update", group: clusterDef.group, resource: "clusters", name: "cluster" }]);

  async function patch(body: unknown) {
    try {
      await kubeClient(http).mergePatch(refOf(clusterDef), undefined, "cluster", body);
      void queryClient.invalidateQueries({ queryKey: listKey(refOf(clusterDef), undefined, {}) });
    } catch (error) {
      toast.show({ tone: "danger", title: t({ id: "action.error.failed", message: "The action failed" }), description: error instanceof Error ? error.message : "" });
    }
  }

  if (clusters.isPending || releases.isPending) {
    return <Spinner />;
  }
  if (!cluster) {
    return <EmptyState title={t({ id: "list.empty.title", message: "Nothing here yet" })} />;
  }
  const phase = textAt(cluster, "status.phase");
  const current = textAt(cluster, "status.version");
  const steps = phaseSteps(phase, textAt(cluster, "status.upgrade.failedPhase"));
  const targets = upgradeTargets(releases.data?.items ?? [], current);
  const failed = phase === "Failed";
  const blocked = conditionOf(cluster, "UpgradeBlocked");
  const writable = canUpdate === "allowed";

  return (
    <>
      <PageHeader
        title={t({ id: "nav.item.upgrades", message: "Upgrades" })}
        actions={
          writable ? (
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
        <dd><Ltr>{textAt(cluster, "spec.desiredVersion")}</Ltr></dd>
        <dt>{t({ id: "col.phase", message: "Phase" })}</dt>
        <dd>{phase ? <PhaseBadge phase={phase} /> : "—"}</dd>
        <dt>{t({ id: "upgrades.window", message: "Maintenance window" })}</dt>
        <dd>{textAt(cluster, "spec.maintenanceWindow") || "—"}</dd>
        <dt>{t({ id: "upgrades.concurrency", message: "Node concurrency" })}</dt>
        <dd>{textAt(cluster, "spec.nodeConcurrency") || "—"}</dd>
      </dl>
      <ol className="br-steps" aria-label={t({ id: "upgrades.timeline", message: "Upgrade phases" })}>
        {steps.map((step) => (
          <li key={step.name} className="br-step" data-state={step.state} aria-current={step.state === "active" ? "step" : undefined}>
            <span>{phaseMessages[step.name] ? i18n._(phaseMessages[step.name] as MessageDescriptor) : step.name}</span>{" "}
            <Badge tone={step.state === "done" ? "success" : step.state === "failed" ? "danger" : step.state === "active" ? "info" : "neutral"}>{i18n._(stateLabels[step.state] as MessageDescriptor)}</Badge>
          </li>
        ))}
      </ol>
      {blocked?.status === "True" ? <p role="status" className="br-help">{blocked.message ?? blocked.reason}</p> : null}
      {writable ? (
        <section className="br-section">
          <h2>{t({ id: "upgrades.start.title", message: "Start an upgrade" })}</h2>
          <Select
            label={t({ id: "upgrades.target", message: "Target version" })}
            value={target}
            options={targets.map((release) => ({ value: textAt(release, "spec.version"), label: textAt(release, "spec.version") }))}
            onValueChange={setTarget}
          />
          <Button variant="primary" disabled={target === ""} onClick={() => setStarting(true)}>
            {t({ id: "upgrades.start.button", message: "Start upgrade" })}
          </Button>
          {failed ? (
            <>
              <Button variant="secondary" onClick={() => void patch({ spec: { upgrade: { action: "resume" } } })}>
                {t({ id: "upgrades.resume", message: "Resume" })}
              </Button>
              <Button variant="danger" onClick={() => setAborting(true)}>
                {t({ id: "upgrades.abort", message: "Abort" })}
              </Button>
            </>
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
      <ConfirmSimpleDialog
        open={starting}
        title={t({ id: "upgrades.start.title", message: "Start an upgrade" })}
        description={t({ id: "upgrades.start.description", message: `Upgrade the cluster to ${target}.` })}
        confirmLabel={t({ id: "upgrades.start.button", message: "Start upgrade" })}
        onConfirm={() => void patch({ spec: { desiredVersion: target } })}
        onCancel={() => setStarting(false)}
      />
      <ConfirmTypedDialog
        open={aborting}
        word="abort"
        title={t({ id: "upgrades.abort.title", message: "Abort the upgrade" })}
        description={t({ id: "upgrades.abort.description", message: "Type abort to stop the failed upgrade." })}
        confirmLabel={t({ id: "upgrades.abort", message: "Abort" })}
        onConfirm={() => {
          void patch({ spec: { upgrade: { action: "abort" } } });
          setAborting(false);
        }}
        onCancel={() => setAborting(false)}
      />
      {editing ? <LazyEditDialog def={clusterDef} object={cluster} onClose={() => setEditing(false)} /> : null}
    </>
  );
}
