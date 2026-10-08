import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { Badge, Button, CodeTextarea, Select, Switch, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow, TextField } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { isHttpError } from "../../api/http";
import { kubeClient, listKey } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { useCan } from "../../authz/authz";
import { ConfirmSimpleDialog } from "../../components/ConfirmDialogs";
import { EmptyState } from "../../components/EmptyState";
import { Ltr } from "../../components/Ltr";
import { PageHeader } from "../../components/PageHeader";
import { Spinner } from "../../components/Spinner";
import { textAt } from "../../resources/path";
import { definitionFor, refOf } from "../../resources/registry";
import { useLiveList } from "../../stream/live";
import { groupSettings, settingMeta } from "./settingsCatalog";

function SettingRow({ setting, canUpdate }: { setting: KubeObject; canUpdate: boolean }) {
  const { t, i18n } = useLingui();
  const http = useHttp();
  const queryClient = useQueryClient();
  const def = definitionFor("settings");
  const key = setting.metadata.name;
  const meta = settingMeta(key);
  const stored = textAt(setting, "spec.value");
  const fallback = textAt(setting, "status.default");
  const [draft, setDraft] = useState(stored !== "" ? stored : fallback);
  const [error, setError] = useState<string | undefined>();
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const effective = stored !== "" ? stored : fallback;
  const usesDefault = stored === "" || stored === fallback;
  const applied = textAt(setting, "status.applied");

  async function save() {
    if (!def) {
      return;
    }
    setBusy(true);
    setError(undefined);
    try {
      await kubeClient(http).mergePatch(refOf(def), undefined, key, { spec: { value: draft } });
      void queryClient.invalidateQueries({ queryKey: listKey(refOf(def), undefined, {}) });
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t({ id: "action.error.failed", message: "The action failed" }));
      if (isHttpError(reason) && reason.status === 403) {
        setError(t({ id: "action.error.forbidden", message: "You are not allowed to do this" }));
      }
    } finally {
      setBusy(false);
    }
  }

  const label = key;
  const control =
    meta.kind === "boolean" ? (
      <Switch label={label} checked={draft === "true"} onCheckedChange={(next) => setDraft(next ? "true" : "false")} disabled={!canUpdate} />
    ) : meta.kind === "enum" ? (
      <Select label={label} value={draft} options={meta.options.map((option) => ({ value: option, label: option }))} onValueChange={setDraft} disabled={!canUpdate} {...(error ? { error } : {})} />
    ) : meta.kind === "multiline" ? (
      <CodeTextarea label={label} value={draft} onChange={setDraft} readOnly={!canUpdate} {...(error ? { error } : {})} />
    ) : (
      <TextField label={label} name={key} value={draft} onChange={setDraft} readOnly={!canUpdate} dir="ltr" {...(error ? { error } : {})} />
    );

  return (
    <TableRow>
      <TableCell>
        <Ltr>{key}</Ltr>
        <div className="br-help">{meta.description ? i18n._(meta.description) : ""}</div>
      </TableCell>
      <TableCell>
        {control}
        {canUpdate ? (
          <Button variant="primary" disabled={busy || draft === effective} onClick={() => (key === "platform.host" ? setConfirming(true) : void save())}>
            {t({ id: "common.action.save", message: "Save" })}
          </Button>
        ) : null}
        {usesDefault ? <span className="br-help">{t({ id: "settings.usesDefault", message: "Uses the default" })}</span> : null}
      </TableCell>
      <TableCell>{fallback === "" ? "—" : <Ltr>{fallback}</Ltr>}</TableCell>
      <TableCell>
        <Badge tone={applied === effective ? "success" : "warning"}>{applied === effective ? t({ id: "settings.applied", message: "Applied" }) : t({ id: "settings.pending", message: "Pending" })}</Badge>
      </TableCell>
      <ConfirmSimpleDialog
        open={confirming}
        title={t({ id: "settings.host.title", message: "Change the platform host" })}
        description={t({ id: "settings.host.description", message: "Sign-in cookies and certificates change with the host name. Users must sign in again on the new name." })}
        confirmLabel={t({ id: "common.action.save", message: "Save" })}
        onConfirm={() => void save()}
        onCancel={() => setConfirming(false)}
      />
    </TableRow>
  );
}

export default function SettingsPage() {
  const { t } = useLingui();
  const def = definitionFor("settings");
  const ref = def ? refOf(def) : { group: "", version: "", plural: "", namespaced: false };
  const query = useLiveList(ref, {});
  const canUpdate = useCan({ verb: "update", group: ref.group, resource: ref.plural }) === "allowed";
  if (query.isPending) {
    return <Spinner />;
  }
  const groups = groupSettings(query.data?.items ?? []);
  return (
    <>
      <PageHeader title={t({ id: "nav.item.settings", message: "Settings" })} />
      {groups.length === 0 ? <EmptyState title={t({ id: "list.empty.title", message: "Nothing here yet" })} /> : null}
      {groups.map((group) => (
        <section key={group.prefix} className="br-section">
          <h2>{group.prefix}</h2>
          <Table caption={group.prefix}>
            <TableHead>
              <TableRow>
                <TableHeaderCell>{t({ id: "settings.col.key", message: "Setting" })}</TableHeaderCell>
                <TableHeaderCell>{t({ id: "col.value", message: "Value" })}</TableHeaderCell>
                <TableHeaderCell>{t({ id: "col.default", message: "Default" })}</TableHeaderCell>
                <TableHeaderCell>{t({ id: "col.status", message: "Status" })}</TableHeaderCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {group.items.map((setting) => (
                <SettingRow key={setting.metadata.uid ?? setting.metadata.name} setting={setting} canUpdate={canUpdate} />
              ))}
            </TableBody>
          </Table>
        </section>
      ))}
    </>
  );
}
