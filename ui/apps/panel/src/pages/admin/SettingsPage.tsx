import { useEffect, useId, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { Badge, Button, CodeTextarea, Select, Switch, TextField } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { isHttpError } from "../../api/http";
import { conditionOf, kubeClient, listKey, type ResourceRef } from "../../api/kube";
import { findPendingWrite, usePendingWrites, type PendingWrite } from "../../api/pendingWrites";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { ConfirmSimpleDialog } from "../../components/ConfirmDialogs";
import { EmptyState } from "../../components/EmptyState";
import { Ltr } from "../../components/Ltr";
import { PageHeader } from "../../components/PageHeader";
import { QueryFailure } from "../../components/QueryFailure";
import { Spinner } from "../../components/Spinner";
import { textAt } from "../../resources/path";
import { definitionFor, refOf } from "../../resources/registry";
import { useLiveList } from "../../stream/live";
import { groupSettings, humanizeSetting, settingMeta } from "./settingsCatalog";
import styles from "./settings.module.css";

function currentReady(setting: KubeObject) {
  const ready = conditionOf(setting, "Ready");
  const generation = setting.metadata.generation;
  const observed = [ready?.observedGeneration, setting.status?.observedGeneration].filter(Boolean);
  return generation === undefined || (observed.length > 0 && observed.every((value) => value === generation)) ? ready : undefined;
}

function SettingRow({ setting, label, description, visible, resource, canUpdate, pending }: {
  setting: KubeObject;
  label: string;
  description: string | undefined;
  visible: boolean;
  resource: ResourceRef;
  canUpdate: boolean;
  pending: PendingWrite | undefined;
}) {
  const { t } = useLingui();
  const http = useHttp();
  const queryClient = useQueryClient();
  const key = setting.metadata.name;
  const heading = useRef<HTMLHeadingElement>(null);
  const editor = useRef<HTMLFormElement>(null);
  const meta = settingMeta(key);
  const stored = textAt(setting, "spec.value");
  const fallback = textAt(setting, "status.default");
  const [edit, setEdit] = useState<{ base: string; fallback: string; value?: string }>();
  const [saved, setSaved] = useState<{ baseVersion: string | undefined; value: string }>();
  const [error, setError] = useState<string>();
  const [confirming, setConfirming] = useState(false);
  const editing = edit !== undefined && canUpdate;
  const busy = pending !== undefined;
  const draft = edit?.value ?? stored;
  const changed = draft !== stored;
  const stale = changed && edit !== undefined && (stored !== edit.base || (edit.base === "" && fallback !== edit.fallback));
  const showingSaved = saved?.baseVersion === setting.metadata.resourceVersion ? saved : undefined;
  const current = showingSaved?.value ?? stored;
  const effective = current === "" ? fallback : current;
  const ready = current === stored ? currentReady(setting) : undefined;
  const editorValue = draft === "" ? fallback : draft;
  const enabled = /^(1|t|T|True|TRUE|true)$/;
  const disabled = /^(0|f|F|False|FALSE|false)$/;
  const notSet = t({ id: "settings.notSet", message: "Not set" });
  const unavailable = "—";

  useEffect(() => {
    if (editing) editor.current?.querySelector<HTMLElement>("input:not([type=hidden]), textarea, [role=switch], [role=combobox]")?.focus();
  }, [editing]);

  function startEdit() {
    setEdit({ base: stored, fallback });
    setSaved(undefined);
    setError(undefined);
  }

  function setDraft(value: string) {
    setEdit((current) => {
      if (!current) return current;
      if (value === stored) return { base: stored, fallback };
      return current.value === undefined || current.value === stored ? { base: stored, fallback, value } : { ...current, value };
    });
    setError(undefined);
  }

  function closeEditor() {
    if (editor.current?.contains(document.activeElement)) heading.current?.focus();
    setEdit(undefined);
    setError(undefined);
  }

  async function save() {
    if (!editing || busy || stale || !changed) return;
    setError(undefined);
    const baseVersion = setting.metadata.resourceVersion;
    try {
      const result = await kubeClient(http).mergePatch(resource, undefined, key, {
        ...(baseVersion ? { metadata: { resourceVersion: baseVersion } } : {}),
        spec: { value: draft },
      });
      setSaved({ baseVersion, value: result?.metadata ? textAt(result, "spec.value") : draft });
      closeEditor();
    } catch (reason) {
      setError(isHttpError(reason) && reason.status === 403
        ? t({ id: "action.error.forbidden", message: "You are not allowed to do this" })
        : reason instanceof Error ? reason.message : t({ id: "action.error.failed", message: "The action failed" }));
      if (isHttpError(reason) && reason.status === 409) {
        await queryClient.invalidateQueries({ queryKey: listKey(resource, undefined, {}) }, { cancelRefetch: false });
      }
    }
  }

  // Ready reports catalog validation, not whether a consuming component has rolled out the value.
  const validation = ready?.status === "True" ? (
    <Badge tone="success">{t({ id: "settings.validated", message: "Validated" })}</Badge>
  ) : ready?.status === "False" ? (
    <Badge tone="danger">{t({ id: "settings.rejected", message: "Rejected" })}</Badge>
  ) : ready?.status === "Unknown" ? (
    <Badge tone="warning">{t({ id: "settings.validationUnknown", message: "Validation unknown" })}</Badge>
  ) : (
    <Badge tone="neutral">{t({ id: "settings.awaitingValidation", message: "Awaiting validation" })}</Badge>
  );
  const value = effective === "" ? notSet
    : meta.kind === "multiline" ? t({ id: "settings.configured", message: "Configured" })
      : meta.kind === "boolean" && enabled.test(effective) ? t({ id: "settings.enabled", message: "Enabled" })
        : meta.kind === "boolean" && disabled.test(effective) ? t({ id: "settings.disabled", message: "Disabled" })
          : <Ltr>{effective}</Ltr>;
  const controlLabel = t({ id: "settings.valueLabel", message: `Value for ${label}` });

  return (
    <li className={styles.row} hidden={!visible}>
      <div className={styles.identity}>
        <h3 ref={heading} tabIndex={-1}>{label}</h3>
        {description ? <p>{description}</p> : null}
      </div>
      <div className={styles.value}>
        <span className={styles.currentValue}>{value}</span>
        {current !== "" ? <span className={styles.source}>{t({ id: "settings.override", message: "Override" })}</span>
          : fallback ? <span className={styles.source}>{t({ id: "settings.usesDefault", message: "Uses the default" })}</span> : null}
      </div>
      <div className={styles.state}>
        {validation}
        {ready?.reason && ready.reason !== "Valid" ? <code><Ltr>{ready.reason}</Ltr></code> : null}
        {ready?.message ? <p>{ready.message}</p> : null}
        {saved && (showingSaved || saved.value === stored) ? <p role="status">{t({ id: "settings.saved", message: "Change saved" })}</p> : null}
      </div>
      <div className={styles.actions}>
        {canUpdate && !editing ? (
          <button className="br-button br-button--secondary" type="button" onClick={startEdit} disabled={busy} aria-label={t({ id: "settings.editLabel", message: `Edit ${label}` })}>
            {t({ id: "common.action.edit", message: "Edit" })}
          </button>
        ) : null}
      </div>
      <details className={styles.details}>
        <summary>{t({ id: "settings.details", message: "Details" })}</summary>
        <dl>
          <dt>{t({ id: "settings.col.key", message: "Key" })}</dt>
          <dd><code><Ltr>{key}</Ltr></code></dd>
          <dt>{t({ id: "col.default", message: "Default" })}</dt>
          <dd>{fallback ? <Ltr>{fallback}</Ltr> : unavailable}</dd>
          {meta.kind === "multiline" && effective ? (
            <>
              <dt>{t({ id: "settings.currentValue", message: "Current value" })}</dt>
              <dd><code><Ltr>{effective}</Ltr></code></dd>
            </>
          ) : null}
          {ready?.reason === "Valid" ? <><dt>{t({ id: "col.status", message: "Status" })}</dt><dd><code><Ltr>{ready.reason}</Ltr></code></dd></> : null}
        </dl>
      </details>
      {editing ? (
        <form className={styles.editor} ref={editor} onSubmit={(event) => {
          event.preventDefault();
          if (!changed || stale || busy) return;
          if (key === "platform.host") setConfirming(true);
          else void save();
        }}>
          {meta.kind === "boolean" ? (
            <Switch label={controlLabel} checked={enabled.test(editorValue)} onCheckedChange={(next) => setDraft(next ? "true" : "false")} disabled={busy} />
          ) : meta.kind === "enum" ? (
            <Select label={controlLabel} value={editorValue} options={meta.options.map((option) => ({ value: option, label: option }))} onValueChange={setDraft} disabled={busy} {...(error ? { error } : {})} />
          ) : meta.kind === "multiline" ? (
            <CodeTextarea label={controlLabel} value={draft} onChange={setDraft} readOnly={busy} {...(error ? { error } : {})} />
          ) : (
            <TextField label={controlLabel} name={key} value={draft} onChange={setDraft} readOnly={busy} dir="ltr" placeholder={fallback || undefined} {...(error ? { error } : {})} />
          )}
          {meta.kind === "boolean" && error ? <p role="alert" className="br-field-error">{error}</p> : null}
          {stale ? <p role="alert">{t({ id: "settings.conflict", message: "This setting changed while you were editing it." })}</p> : null}
          <div className={styles.editorActions}>
            {changed && !stale ? (
              <Button type="submit" variant="primary" disabled={busy}>
                {busy ? t({ id: "pendingWrites.update", message: "Saving" }) : t({ id: "common.action.save", message: "Save" })}
              </Button>
            ) : null}
            {stale ? (
              <Button variant="secondary" onClick={startEdit}>{t({ id: "settings.useCurrent", message: "Use current value" })}</Button>
            ) : draft !== "" && fallback ? (
              <Button variant="secondary" disabled={busy} onClick={() => setDraft("")}>{t({ id: "settings.useDefault", message: "Use default" })}</Button>
            ) : changed && fallback ? <span>{t({ id: "settings.usesDefault", message: "Uses the default" })}</span> : null}
            <Button variant="secondary" disabled={busy} onClick={closeEditor}>{t({ id: "common.action.cancel", message: "Cancel" })}</Button>
          </div>
        </form>
      ) : null}
      {confirming ? (
        <ConfirmSimpleDialog
          open
          title={t({ id: "settings.host.title", message: "Change the platform host" })}
          description={t({ id: "settings.host.description", message: "Sign-in cookies and certificates change with the host name. Users must sign in again on the new name." })}
          confirmLabel={t({ id: "common.action.save", message: "Save" })}
          onConfirm={() => void save()}
          onCancel={() => setConfirming(false)}
        />
      ) : null}
    </li>
  );
}

const normalize = (text: string) => text.normalize("NFKC").toLowerCase();

export default function SettingsPage() {
  const { t, i18n } = useLingui();
  const http = useHttp();
  const writes = usePendingWrites(http);
  const def = definitionFor("settings");
  const resource = def ? refOf(def) : { group: "", version: "", plural: "", namespaced: false };
  const query = useLiveList(resource, {});
  const items = query.data?.items ?? [];
  const [search, setSearch] = useState("");
  const [category, setCategory] = useState("platform");
  const searchId = useId();
  const categoryId = useId();
  const title = t({ id: "nav.item.settings", message: "Settings" });

  const terms = normalize(search).trim().split(/\s+/).filter(Boolean);
  const groups = groupSettings(items);
  const selected = groups.some((group) => group.prefix === category) ? category : groups[0]?.prefix;
  const visibleNames: string[] = [];
  const visibleGroups = groups.map((group) => {
    const groupLabel = group.label ? i18n._(group.label) : humanizeSetting(group.prefix);
    const rows = group.items.map((setting) => {
      const key = setting.metadata.name;
      const meta = settingMeta(key);
      const label = meta.label ? i18n._(meta.label) : humanizeSetting(key.slice(group.prefix.length + 1) || key);
      const description = meta.description ? i18n._(meta.description) : undefined;
      const text = normalize([key, group.prefix, groupLabel, group.label?.message, label, meta.label?.message, description, meta.description?.message].join(" "));
      const visible = terms.length ? terms.every((term) => text.includes(term)) : group.prefix === selected;
      if (visible) visibleNames.push(key);
      return { setting, label, description, visible };
    });
    return { prefix: group.prefix, groupLabel, rows, count: rows.filter((row) => row.visible).length };
  });
  const access = useCans(visibleNames.map((name) => ({ verb: "patch", group: resource.group, resource: resource.plural, name })));
  const editable = new Set(visibleNames.filter((_, index) => access[index] === "allowed"));
  const numbers = new Intl.NumberFormat(i18n.locale);
  const count = numbers.format(visibleNames.length);
  const categoryLabel = t({ id: "settings.categories", message: "Categories" });
  const searchLabel = t({ id: "settings.search", message: "Search all settings" });

  if (query.isPending) return <Spinner />;
  if (query.isError && !query.data) {
    return <><PageHeader title={title} /><QueryFailure error={query.error} retry={() => void query.refetch()} /></>;
  }

  function chooseCategory(prefix: string) {
    setCategory(prefix);
    setSearch("");
  }

  return (
    <>
      <PageHeader title={title} description={t({ id: "settings.description", message: "Manage cluster-wide configuration and see which values pass validation." })} />
      {query.isError ? <QueryFailure error={query.error} retry={() => void query.refetch()} /> : null}
      {items.length === 0 ? <EmptyState title={t({ id: "list.empty.title", message: "Nothing here yet" })} /> : (
        <div className={styles.layout}>
          <nav className={styles.categories} aria-label={categoryLabel}>
            <h2>{categoryLabel}</h2>
            <ul role="list">
              {visibleGroups.map((group) => (
                <li key={group.prefix}>
                  <button type="button" aria-current={!terms.length && group.prefix === selected ? "true" : undefined} onClick={() => chooseCategory(group.prefix)}>
                    <span>{group.groupLabel}</span>
                    <span>{numbers.format(group.rows.length)}</span>
                  </button>
                </li>
              ))}
            </ul>
          </nav>
          <div className={styles.content}>
            <div className={styles.toolbar}>
              <label className={styles.search} htmlFor={searchId}>
                <span>{searchLabel}</span>
                <input className="br-field-input" id={searchId} type="search" value={search} onChange={(event) => setSearch(event.target.value)} />
              </label>
              <label className={styles.categorySelect} htmlFor={categoryId}>
                <span>{categoryLabel}</span>
                <select className="br-field-input" id={categoryId} value={terms.length ? "" : selected} onChange={(event) => chooseCategory(event.target.value)}>
                  {terms.length ? <option value="" disabled>{t({ id: "settings.searchResults", message: "Search results" })}</option> : null}
                  {visibleGroups.map((group) => <option key={group.prefix} value={group.prefix}>{group.groupLabel}</option>)}
                </select>
              </label>
              <p role="status">{t({ id: "settings.resultCount", message: `Settings shown: ${count}` })}</p>
            </div>
            {visibleNames.length === 0 ? <EmptyState title={t({ id: "list.noMatch.title", message: "No match" })} /> : null}
            {visibleGroups.map((group) => (
              <section key={group.prefix} className={styles.section} hidden={group.count === 0}>
                <h2>{group.groupLabel}</h2>
                <ul role="list">
                  {group.rows.map((row) => (
                    <SettingRow
                      key={row.setting.metadata.uid ?? row.setting.metadata.name}
                      {...row}
                      resource={resource}
                      canUpdate={editable.has(row.setting.metadata.name)}
                      pending={findPendingWrite(writes, resource, row.setting)}
                    />
                  ))}
                </ul>
              </section>
            ))}
          </div>
        </div>
      )}
    </>
  );
}
