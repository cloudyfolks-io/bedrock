import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { Badge, Button, CodeTextarea, Switch, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { conditionOf, kubeClient, listKey, resourcePath } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { useCan } from "../../authz/authz";
import { EmptyState } from "../../components/EmptyState";
import { PageHeader } from "../../components/PageHeader";
import { QueryFailure } from "../../components/QueryFailure";
import { Spinner } from "../../components/Spinner";
import { definitionFor, refOf } from "../../resources/registry";
import { useLiveList } from "../../stream/live";

function AddonCard({ addon, canUpdate }: { addon: KubeObject; canUpdate: boolean }) {
  const { t } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const queryClient = useQueryClient();
  const def = definitionFor("addons");
  const name = addon.metadata.name;
  const enabled = addon.spec?.enabled === true;
  const ready = conditionOf(addon, "Ready");
  const savedValues = addon.spec?.values === undefined ? "" : JSON.stringify(addon.spec.values, null, 2);
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
      await queryClient.invalidateQueries({ queryKey: listKey(refOf(def), undefined, {}) });
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
      const path = resourcePath(refOf(def), { name }, { fieldManager: "bedrock-panel" });
      setError(undefined);
      if (await update(() => http.request("PATCH", path, { body, contentType: "application/json-patch+json" }))) {
        setDraft(undefined);
      }
    } catch {
      setError(t({ id: "forms.json.invalid", message: "This is not valid JSON" }));
    }
  }

  return (
    <section className="br-card-section" aria-label={name}>
      <h2>{name}</h2>
      {canUpdate ? (
        <Switch label={t({ id: "addons.enabled", message: "Enabled" })} checked={enabled} onCheckedChange={setEnabled} disabled={busy} />
      ) : (
        <Badge tone={enabled ? "success" : "neutral"}>{enabled ? t({ id: "addons.on", message: "On" }) : t({ id: "addons.off", message: "Off" })}</Badge>
      )}
      <p>
        <Badge tone={ready?.status === "True" ? "success" : ready?.status === "False" ? "danger" : "neutral"}>
          {ready?.status === "True" ? t({ id: "status.condition.ready", message: "Ready" }) : ready?.status === "False" ? t({ id: "status.condition.notReady", message: "Not ready" }) : t({ id: "status.condition.none", message: "No status" })}
        </Badge>{" "}
        {ready?.message ?? ""}
      </p>
      <CodeTextarea label={`${name} values`} value={text} onChange={(value) => setDraft((current) => value === savedValues ? undefined : { base: current?.base ?? savedValues, value })} readOnly={!canUpdate || busy} {...(error ? { error } : {})} />
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
    </section>
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
    <>
      <PageHeader title={t({ id: "nav.item.addons", message: "Addons" })} />
      {query.isPending ? <Spinner /> : null}
      {query.isError ? <QueryFailure error={query.error} retry={() => void query.refetch()} /> : null}
      {query.isSuccess && addons.length === 0 ? <EmptyState title={t({ id: "list.empty.title", message: "Nothing here yet" })} /> : null}
      {query.isSuccess ? addons.map((addon) => (
        <AddonCard key={addon.metadata.uid ?? addon.metadata.name} addon={addon} canUpdate={canUpdate} />
      )) : null}
    </>
  );
}
