import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { Badge, Button, CodeTextarea, Switch, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { conditionOf, kubeClient, listKey, resourcePath } from "../../api/kube";
import { trackWrite } from "../../api/pendingWrites";
import type { KubeObject } from "../../api/types";
import { useCan } from "../../authz/authz";
import { EmptyState } from "../../components/EmptyState";
import { PageHeader } from "../../components/PageHeader";
import { QueryFailure } from "../../components/QueryFailure";
import { Spinner } from "../../components/Spinner";
import { definitionFor, refOf } from "../../resources/registry";
import { useLiveList } from "../../stream/live";
import { PendingWriteTag } from "../../components/PendingWrites";
import { Ltr } from "../../components/Ltr";
import styles from "../operator.module.css";
import { OperatorSection } from "./OperatorSection";

function AddonCard({ addon, canUpdate }: { addon: KubeObject; canUpdate: boolean }) {
  const { t } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const queryClient = useQueryClient();
  const def = definitionFor("addons");
  const name = addon.metadata.name;
  const enabled = addon.spec?.enabled === true;
  const ready = conditionOf(addon, "Ready");
  const values = addon.spec?.values;
  const hasValues = values != null && Object.keys(values).length > 0;
  const savedValues = hasValues ? JSON.stringify(values, null, 2) : "";
  const [addingValues, setAddingValues] = useState(false);
  const [draft, setDraft] = useState<{ base: string; value: string }>();
  const text = draft?.value ?? savedValues;
  const stale = draft !== undefined && draft.base !== savedValues && text !== savedValues;
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);

  async function update(apply: () => Promise<unknown>): Promise<boolean> {
    if (!def || !canUpdate || busy) {
      return false;
    }
    setBusy(true);
    try {
      await apply();
      toast.show({ tone: "success", title: t({ id: "action.accepted", message: "Request accepted" }) });
      await queryClient.invalidateQueries({ queryKey: listKey(refOf(def), undefined, {}) }, { cancelRefetch: false });
      return true;
    } catch (reason) {
      toast.show({ tone: "danger", title: t({ id: "action.error.failed", message: "The action failed" }), description: reason instanceof Error ? reason.message : "" });
      return false;
    } finally {
      setBusy(false);
    }
  }

  function setEnabled(value: boolean) {
    if (def) {
      void update(() => kubeClient(http).mergePatch(refOf(def), undefined, name, { spec: { enabled: value } }));
    }
  }

  async function saveValues() {
    if (!def || stale) {
      return;
    }
    try {
      const parsed: unknown = text.trim() === "" ? null : JSON.parse(text);
      const version = addon.metadata.resourceVersion;
      const values = parsed === null ? { op: "remove", path: "/spec/values" } : { op: "add", path: "/spec/values", value: parsed };
      if (parsed === null && addon.spec?.values === undefined) {
        setDraft(undefined);
        setError(undefined);
        return;
      }
      const body = version ? [{ op: "test", path: "/metadata/resourceVersion", value: version }, values] : [values];
      const ref = refOf(def);
      const path = resourcePath(ref, { name }, { fieldManager: "bedrock-panel" });
      setError(undefined);
      if (await update(() => trackWrite(http, { kind: "update", ref, namespace: undefined, name }, () =>
        http.request("PATCH", path, { body, contentType: "application/json-patch+json" }),
      ))) {
        setDraft(undefined);
        setAddingValues(false);
      }
    } catch {
      setError(t({ id: "forms.json.invalid", message: "This is not valid JSON" }));
    }
  }

  return (
    <OperatorSection title={name} summary={
      <div className={styles.health}>
        {t({ id: "addons.health", message: "Health" })}
        <Badge tone={ready?.status === "True" ? "success" : ready?.reason === "Disabled" ? "neutral" : ready?.status === "False" ? "danger" : ready ? "warning" : "neutral"}>
          {ready?.status === "True" ? t({ id: "status.condition.ready", message: "Ready" })
            : ready?.status === "False" ? t({ id: "status.condition.notReady", message: "Not ready" })
            : ready ? t({ id: "status.condition.unknown", message: "Unknown" })
            : t({ id: "status.condition.none", message: "No status" })}
        </Badge>
        {ready?.reason && ready.reason !== "Ready" ? <Ltr>{ready.reason}</Ltr> : null}
        {ready?.message ? <bdi>{ready.message}</bdi> : null}
      </div>
    } actions={
      <div className={styles.actions}>
        {busy ? <PendingWriteTag kind="update" /> : null}
        {canUpdate ? (
          <Switch label={t({ id: "addons.enabled", message: "Enabled" })} checked={enabled} onCheckedChange={setEnabled} disabled={busy} />
        ) : (
          <Badge tone="neutral">{enabled ? t({ id: "addons.on", message: "On" }) : t({ id: "addons.off", message: "Off" })}</Badge>
        )}
      </div>
    }>
      {hasValues || canUpdate ? (
        <details className={styles.advanced}>
          <summary>
            {t({ id: "addons.advancedValues", message: "Advanced values (JSON)" })}
            {!hasValues ? <span className={styles.help}>{t({ id: "addons.noCustomValues", message: "No custom values" })}</span> : null}
          </summary>
          <div className={styles.sectionBody}>
            {hasValues || draft || (canUpdate && addingValues) ? (
              <>
                <CodeTextarea label={t({ id: "addons.valuesLabel", message: `${name} values` })} value={text} onChange={(value) => setDraft((current) => value === savedValues ? undefined : { base: current?.base ?? savedValues, value })} readOnly={!canUpdate || busy} {...(error ? { error } : {})} />
                {canUpdate ? (
                  <Button variant="primary" onClick={() => void saveValues()} disabled={busy || stale || text === savedValues}>
                    {t({ id: "addons.saveValues", message: "Save values" })}
                  </Button>
                ) : null}
                {stale ? (
                  <>
                    <p role="alert">{t({ id: "addons.conflict", message: "The addon values changed while you were editing them." })}</p>
                    <Button variant="secondary" onClick={() => setDraft(undefined)}>{t({ id: "settings.useCurrent", message: "Use current value" })}</Button>
                  </>
                ) : null}
              </>
            ) : <Button variant="secondary" disabled={busy} onClick={() => setAddingValues(true)}>{t({ id: "addons.addValues", message: "Add custom values" })}</Button>}
          </div>
        </details>
      ) : <p className={`${styles.sectionBody} ${styles.help}`}>{t({ id: "addons.noCustomValues", message: "No custom values" })}</p>}
    </OperatorSection>
  );
}

export default function AddonsPage() {
  const { t } = useLingui();
  const def = definitionFor("addons");
  const ref = def ? refOf(def) : { group: "", version: "", plural: "", namespaced: false };
  const query = useLiveList(ref, {});
  const canUpdate = useCan({ verb: "patch", group: ref.group, resource: ref.plural }) === "allowed";
  const addons = query.data?.items ?? [];
  return (
    <div className={styles.stack}>
      <PageHeader title={t({ id: "nav.item.addons", message: "Addons" })} />
      {query.isPending ? <Spinner /> : null}
      {query.isError ? <QueryFailure error={query.error} retry={() => void query.refetch()} /> : null}
      {query.isSuccess && addons.length === 0 ? <EmptyState title={t({ id: "list.empty.title", message: "Nothing here yet" })} /> : null}
      {query.isSuccess ? addons.map((addon) => (
        <AddonCard key={addon.metadata.uid ?? addon.metadata.name} addon={addon} canUpdate={canUpdate} />
      )) : null}
    </div>
  );
}
