import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Dialog, TextField, useToast } from "@bedrock/design";
import { useHttp } from "../api/HttpProvider";
import { isHttpError, isKubeStatusError } from "../api/http";
import { kubeClient, listKey, objectKey } from "../api/kube";
import { useOpenAPIDocument } from "../api/openapi";
import type { KubeObject } from "../api/types";
import { Spinner } from "../components/Spinner";
import { useCurrentProject } from "../shell/context";
import { refOf, type ResourceDef } from "../resources/registry";
import { causesToErrors, splitCauses, type Cause } from "./errors";
import { markReadonly, toFormSchema } from "./schema";
import { CreateForm, EditForm } from "./SchemaForm";
import type { ErrorSchema } from "@rjsf/utils";

const dnsLabel = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

interface WriteErrors {
  extraErrors: ErrorSchema | undefined;
  formErrors: string[];
  nameError: string | undefined;
}

const noErrors: WriteErrors = { extraErrors: undefined, formErrors: [], nameError: undefined };

function useDocumentSchema(def: ResourceDef) {
  const document = useOpenAPIDocument(def.group, def.version);
  const form = useMemo(() => (document.data ? toFormSchema(document.data, def.kind) : undefined), [document.data, def.kind]);
  return { document, form };
}

function useWriteHandling() {
  const { t } = useLingui();
  const toast = useToast();
  return (error: unknown, schema: Parameters<typeof splitCauses>[1]): WriteErrors | "conflict" => {
    if (isKubeStatusError(error) && error.reason === "Invalid") {
      const nameCauses = error.causes.filter((cause) => cause.field === "metadata.name");
      const { mapped, unmapped } = splitCauses(error.causes.filter((cause) => cause.field !== "metadata.name"), schema);
      return {
        extraErrors: causesToErrors(mapped),
        formErrors: unmapped.map((cause: Cause) => `${cause.field}: ${cause.message}`),
        nameError: nameCauses[0]?.message,
      };
    }
    if (isKubeStatusError(error) && error.reason === "AlreadyExists") {
      return { ...noErrors, nameError: t({ id: "forms.name.exists", message: "An object with this name already exists" }) };
    }
    if (isKubeStatusError(error) && error.reason === "Conflict") {
      return "conflict";
    }
    toast.show({
      tone: "danger",
      title: isHttpError(error) && error.status === 403 ? t({ id: "action.error.forbidden", message: "You are not allowed to do this" }) : t({ id: "action.error.failed", message: "The action failed" }),
      description: error instanceof Error ? error.message : "",
    });
    return noErrors;
  };
}

interface CreateProps {
  def: ResourceDef;
  onClose: () => void;
  initialSpec?: Record<string, unknown>;
  lockedPaths?: string[];
}

export function CreateResourceDialog({ def, onClose, initialSpec, lockedPaths = [] }: CreateProps) {
  const { t, i18n } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const queryClient = useQueryClient();
  const project = useCurrentProject();
  const kind = def.kind;
  const { document, form } = useDocumentSchema(def);
  const [name, setName] = useState("");
  const [errors, setErrors] = useState<WriteErrors>(noErrors);
  const [busy, setBusy] = useState(false);
  const handle = useWriteHandling();
  const ref = refOf(def);
  const namespace = def.scope === "Namespaced" ? project?.namespace : undefined;

  async function submit(spec: Record<string, unknown>) {
    if (!dnsLabel.test(name) || name.length > 63) {
      setErrors({ ...noErrors, nameError: t({ id: "forms.name.invalid", message: "Use lowercase letters, digits and hyphens. Start and end with a letter or digit." }) });
      return;
    }
    const object: KubeObject = {
      apiVersion: `${def.group}/${def.version}`,
      kind: def.kind,
      metadata: { name, ...(namespace === undefined ? {} : { namespace }) },
      spec,
    };
    setBusy(true);
    try {
      await kubeClient(http).create(ref, namespace, object);
      toast.show({ tone: "success", title: t({ id: "forms.create.done", message: `Created ${name}` }) });
      void queryClient.invalidateQueries({ queryKey: listKey(ref, namespace, {}) });
      onClose();
    } catch (error) {
      const result = handle(error, form?.schema ?? {});
      setErrors(result === "conflict" ? noErrors : result);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) {
          onClose();
        }
      }}
      title={t({ id: "forms.create.title", message: `Create ${kind}` })}
      description={i18n._(def.title)}
    >
      {document.isPending ? <Spinner /> : null}
      {document.isError ? (
        <Alert tone="error">
          {t({ id: "forms.schema.error", message: "The form schema did not load." })}{" "}
          <Button variant="secondary" onClick={() => void document.refetch()}>
            {t({ id: "common.action.retry", message: "Retry" })}
          </Button>
        </Alert>
      ) : null}
      {form ? (
        <>
          <TextField
            label={t({ id: "forms.name.label", message: "Name" })}
            name="metadata-name"
            autoComplete="off"
            value={name}
            onChange={setName}
            dir="ltr"
            {...(errors.nameError ? { error: errors.nameError } : {})}
          />
          <CreateForm
            schema={form.schema}
            uiSchema={markReadonly(form.uiSchema, lockedPaths)}
            formData={initialSpec}
            onSubmit={(spec) => void submit(spec)}
            extraErrors={errors.extraErrors}
            formErrors={errors.formErrors}
            submitLabel={t({ id: "common.action.create", message: "Create" })}
            busy={busy}
          />
        </>
      ) : null}
    </Dialog>
  );
}

interface EditProps {
  def: ResourceDef;
  object: KubeObject;
  onClose: () => void;
}

function EditBody({ def, object, onClose }: EditProps) {
  const { t } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const queryClient = useQueryClient();
  const { document, form } = useDocumentSchema(def);
  const ref = refOf(def);
  const { namespace, name } = object.metadata;
  const latest = useQuery({
    queryKey: ["edit", ...objectKey(ref, namespace, name)],
    queryFn: () => kubeClient(http).get(ref, namespace, name),
    staleTime: 0,
    gcTime: 0,
  });
  const [errors, setErrors] = useState<WriteErrors>(noErrors);
  const [conflict, setConflict] = useState(false);
  const [busy, setBusy] = useState(false);
  const handle = useWriteHandling();

  async function submit(spec: Record<string, unknown>) {
    const base = latest.data ?? object;
    const body: KubeObject = { apiVersion: `${def.group}/${def.version}`, kind: def.kind, metadata: base.metadata, spec };
    setBusy(true);
    try {
      await kubeClient(http).replace(ref, body);
      toast.show({ tone: "success", title: t({ id: "forms.edit.done", message: `Saved ${name}` }) });
      void queryClient.invalidateQueries({ queryKey: listKey(ref, namespace, {}) });
      void queryClient.invalidateQueries({ queryKey: objectKey(ref, namespace, name) });
      onClose();
    } catch (error) {
      const result = handle(error, form?.schema ?? {});
      if (result === "conflict") {
        setConflict(true);
      } else {
        setErrors(result);
      }
    } finally {
      setBusy(false);
    }
  }

  const ready = form && latest.data;
  return (
    <>
      {document.isPending || latest.isPending ? <Spinner /> : null}
      {latest.isError || document.isError ? <Alert tone="error">{t({ id: "forms.schema.error", message: "The form schema did not load." })}</Alert> : null}
      {conflict ? (
        <Alert tone="error">
          {t({ id: "forms.conflict.message", message: "Someone else changed this object. Reload to continue. Your edits stay in the form." })}{" "}
          <Button
            variant="secondary"
            onClick={() => {
              void latest.refetch().then(() => setConflict(false));
            }}
          >
            {t({ id: "forms.conflict.reload", message: "Reload" })}
          </Button>
        </Alert>
      ) : null}
      {ready ? (
        <EditForm
          schema={form.schema}
          uiSchema={form.uiSchema}
          immutable={form.immutable}
          formData={(latest.data?.spec ?? {}) as Record<string, unknown>}
          onSubmit={(spec) => void submit(spec)}
          extraErrors={errors.extraErrors}
          formErrors={errors.formErrors}
          submitLabel={t({ id: "common.action.save", message: "Save" })}
          busy={busy}
        />
      ) : null}
    </>
  );
}

export function EditResourceDialog({ def, object, onClose }: EditProps) {
  const { t } = useLingui();
  const { name } = object.metadata;
  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) {
          onClose();
        }
      }}
      title={t({ id: "forms.edit.title", message: `Edit ${name}` })}
    >
      <EditBody def={def} object={object} onClose={onClose} />
    </Dialog>
  );
}
