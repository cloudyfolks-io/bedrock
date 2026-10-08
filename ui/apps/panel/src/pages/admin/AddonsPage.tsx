import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { Badge, Button, CodeTextarea, Switch, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { conditionOf, kubeClient, listKey } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { useCan } from "../../authz/authz";
import { PageHeader } from "../../components/PageHeader";
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
  const [text, setText] = useState(addon.spec?.values === undefined ? "" : JSON.stringify(addon.spec.values, null, 2));
  const [error, setError] = useState<string | undefined>();

  async function patch(spec: Record<string, unknown>) {
    if (!def) {
      return;
    }
    try {
      await kubeClient(http).mergePatch(refOf(def), undefined, name, { spec });
      void queryClient.invalidateQueries({ queryKey: listKey(refOf(def), undefined, {}) });
    } catch (reason) {
      toast.show({ tone: "danger", title: t({ id: "action.error.failed", message: "The action failed" }), description: reason instanceof Error ? reason.message : "" });
    }
  }

  function saveValues() {
    try {
      const parsed: unknown = text.trim() === "" ? null : JSON.parse(text);
      setError(undefined);
      void patch({ values: parsed });
    } catch {
      setError(t({ id: "forms.json.invalid", message: "This is not valid JSON" }));
    }
  }

  return (
    <section className="br-card-section" aria-label={name}>
      <h2>{name}</h2>
      {canUpdate ? (
        <Switch label={t({ id: "addons.enabled", message: "Enabled" })} checked={enabled} onCheckedChange={(next) => void patch({ enabled: next })} />
      ) : (
        <Badge tone={enabled ? "success" : "neutral"}>{enabled ? t({ id: "addons.on", message: "On" }) : t({ id: "addons.off", message: "Off" })}</Badge>
      )}
      <p>
        <Badge tone={ready?.status === "True" ? "success" : ready?.status === "False" ? "danger" : "neutral"}>
          {ready?.status === "True" ? t({ id: "status.condition.ready", message: "Ready" }) : ready?.status === "False" ? t({ id: "status.condition.notReady", message: "Not ready" }) : t({ id: "status.condition.none", message: "No status" })}
        </Badge>{" "}
        {ready?.message ?? ""}
      </p>
      <CodeTextarea label={`${name} values`} value={text} onChange={setText} readOnly={!canUpdate} {...(error ? { error } : {})} />
      {canUpdate ? (
        <Button variant="primary" onClick={saveValues}>
          {t({ id: "addons.saveValues", message: "Save values" })}
        </Button>
      ) : null}
    </section>
  );
}

export default function AddonsPage() {
  const { t } = useLingui();
  const def = definitionFor("addons");
  const ref = def ? refOf(def) : { group: "", version: "", plural: "", namespaced: false };
  const query = useLiveList(ref, {});
  const canUpdate = useCan({ verb: "update", group: ref.group, resource: ref.plural }) === "allowed";
  if (query.isPending) {
    return <Spinner />;
  }
  return (
    <>
      <PageHeader title={t({ id: "nav.item.addons", message: "Addons" })} />
      {(query.data?.items ?? []).map((addon) => (
        <AddonCard key={addon.metadata.uid ?? addon.metadata.name} addon={addon} canUpdate={canUpdate} />
      ))}
    </>
  );
}
