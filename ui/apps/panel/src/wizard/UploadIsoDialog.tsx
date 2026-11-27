import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { Button, Dialog, TextField, useToast } from "@bedrock/design";
import { useHttp } from "../api/HttpProvider";
import { kubeClient, objectKey } from "../api/kube";
import type { KubeObject } from "../api/types";
import { ImageUpload } from "../pages/project/ImageUpload";
import { definitionFor, refOf } from "../resources/registry";

const dnsLabel = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

interface Props {
  namespace: string;
  onClose: () => void;
  onUploaded: (name: string) => void;
}

export function UploadIsoDialog({ namespace, onClose, onUploaded }: Props) {
  const { t } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const queryClient = useQueryClient();
  const def = definitionFor("images");
  const [name, setName] = useState("");
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<string | undefined>();
  const ref = def ? refOf(def) : { group: "", version: "", plural: "", namespaced: true };
  const image = useQuery({
    queryKey: objectKey(ref, namespace, created ?? ""),
    enabled: created !== undefined,
    refetchInterval: (query) => ((query.state.data as KubeObject | undefined)?.status?.phase === "WaitingForUpload" ? false : 1000),
    queryFn: () => kubeClient(http).get(ref, namespace, created ?? ""),
  });

  async function create() {
    setCreating(true);
    try {
      await kubeClient(http).create(ref, namespace, {
        apiVersion: "bedrock.cloudyfolks.io/v1alpha1",
        kind: "Image",
        metadata: { name, namespace },
        spec: { type: "iso", source: { upload: {} } },
      });
      setCreated(name);
      toast.show({ tone: "success", title: t({ id: "action.accepted", message: "Request accepted" }) });
    } catch (error) {
      toast.show({ tone: "danger", title: t({ id: "action.error.failed", message: "The action failed" }), description: error instanceof Error ? error.message : "" });
    } finally {
      setCreating(false);
    }
  }

  if (created !== undefined && image.data?.status?.phase === "WaitingForUpload") {
    return (
      <ImageUpload
        image={image.data}
        onClose={onClose}
        onUploaded={() => {
          void queryClient.invalidateQueries({ queryKey: ["wizard", "images", namespace] });
          onUploaded(created);
        }}
      />
    );
  }
  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) {
          onClose();
        }
      }}
      title={t({ id: "wizard.uploadIso", message: "Upload a new ISO" })}
      actions={
        <Button variant="primary" disabled={!dnsLabel.test(name) || creating || created !== undefined} onClick={() => void create()}>
          {t({ id: "common.action.create", message: "Create" })}
        </Button>
      }
    >
      <TextField label={t({ id: "forms.name.label", message: "Name" })} name="iso-name" dir="ltr" autoComplete="off" value={name} onChange={setName} disabled={creating || created !== undefined} />
      {created !== undefined ? <p role="status">{t({ id: "wizard.uploadIso.waiting", message: "Preparing the upload" })}</p> : null}
    </Dialog>
  );
}
