import { useRef, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { useToast } from "@bedrock/design";
import { useHttp } from "../api/HttpProvider";
import { isHttpError } from "../api/http";
import { kubeClient, listKey, objectKey } from "../api/kube";
import type { KubeObject } from "../api/types";
import { ConfirmDeleteDialog, ConfirmSimpleDialog } from "../components/ConfirmDialogs";
import { refOf, type ActionDef, type ResourceDef } from "./registry";

interface Pending {
  action: ActionDef;
  object: KubeObject;
}

export interface ActionRunner {
  request: (action: ActionDef, object: KubeObject) => void;
  dialogs: ReactNode;
}

export function useActionRunner(def: ResourceDef): ActionRunner {
  const { t, i18n } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [pending, setPending] = useState<Pending | undefined>();
  const busy = useRef(new Set<string>());
  const ref = refOf(def);
  const client = kubeClient(http);

  async function perform(action: ActionDef, object: KubeObject): Promise<string | undefined> {
    const { namespace, name } = object.metadata;
    if (action.kind === "subresource") {
      return client.postSubresource(ref, namespace, name, action.subresource ?? action.id);
    }
    if (action.kind === "patch") {
      await client.mergePatch(ref, namespace, name, action.patch?.(object) ?? {});
      return undefined;
    }
    await client.remove(ref, namespace, name);
    return undefined;
  }

  async function execute(action: ActionDef, object: KubeObject) {
    const key = `${action.id}:${object.metadata.uid ?? object.metadata.name}`;
    if (busy.current.has(key)) {
      return;
    }
    busy.current.add(key);
    try {
      const message = await perform(action, object);
      toast.show({ tone: "success", title: i18n._(action.label), ...(message ? { description: message } : {}) });
      void queryClient.invalidateQueries({ queryKey: listKey(ref, object.metadata.namespace, {}) });
      void queryClient.invalidateQueries({ queryKey: objectKey(ref, object.metadata.namespace, object.metadata.name) });
    } catch (error) {
      const forbidden = isHttpError(error) && error.status === 403;
      toast.show({
        tone: "danger",
        title: forbidden ? t({ id: "action.error.forbidden", message: "You are not allowed to do this" }) : t({ id: "action.error.failed", message: "The action failed" }),
        description: forbidden || !(error instanceof Error) ? "" : error.message,
      });
    } finally {
      busy.current.delete(key);
    }
  }

  function request(action: ActionDef, object: KubeObject) {
    if (action.confirm === "none") {
      void execute(action, object);
      return;
    }
    setPending({ action, object });
  }

  const close = () => setPending(undefined);
  const confirm = () => {
    if (pending) {
      void execute(pending.action, pending.object);
    }
    close();
  };

  const dialogs = pending ? (
    pending.action.confirm === "typed" ? (
      <ConfirmDeleteDialog name={pending.object.metadata.name} kind={def.kind} open onConfirm={confirm} onCancel={close} />
    ) : (
      <ConfirmSimpleDialog
        title={i18n._(pending.action.label)}
        description={t({ id: "confirm.simple.description", message: `${i18n._(pending.action.label)}: ${pending.object.metadata.name}` })}
        confirmLabel={i18n._(pending.action.label)}
        open
        onConfirm={confirm}
        onCancel={close}
      />
    )
  ) : null;

  return { request, dialogs };
}
