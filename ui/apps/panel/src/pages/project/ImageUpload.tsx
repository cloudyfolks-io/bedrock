import { useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Dialog, Progress, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { listKey } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { definitionFor, refOf } from "../../resources/registry";
import { requestUploadToken, uploadFile } from "./imageUpload";

export function ImageUpload({ image, onClose, onUploaded }: { image: KubeObject; onClose: () => void; onUploaded?: () => void }) {
  const { t } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const queryClient = useQueryClient();
  const def = definitionFor("images");
  const [file, setFile] = useState<File | undefined>();
  const [progress, setProgress] = useState<{ loaded: number; total: number } | undefined>();
  const [error, setError] = useState<string | undefined>();
  const [running, setRunning] = useState(false);
  const controller = useRef<AbortController | undefined>(undefined);

  async function start() {
    if (!file || !image.metadata.namespace) {
      return;
    }
    setError(undefined);
    setRunning(true);
    const abort = new AbortController();
    controller.current = abort;
    try {
      const grant = await requestUploadToken(http, image.metadata.namespace, image.metadata.name);
      await uploadFile(grant.url, grant.token, file, (loaded, total) => setProgress({ loaded, total }), abort.signal);
      toast.show({ tone: "success", title: t({ id: "images.upload.done", message: "Upload finished" }) });
      if (def) {
        void queryClient.invalidateQueries({ queryKey: listKey(refOf(def), image.metadata.namespace, {}) });
      }
      onUploaded?.();
      onClose();
    } catch (reason) {
      if (!(reason instanceof DOMException && reason.name === "AbortError")) {
        setError(reason instanceof Error ? reason.message : t({ id: "action.error.failed", message: "The action failed" }));
      }
    } finally {
      setRunning(false);
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) {
          controller.current?.abort();
          onClose();
        }
      }}
      title={t({ id: "images.upload.title", message: `Upload ${image.metadata.name}` })}
      actions={
        <>
          {running ? (
            <Button variant="secondary" onClick={() => controller.current?.abort()}>
              {t({ id: "common.action.cancel", message: "Cancel" })}
            </Button>
          ) : null}
          <Button variant="primary" disabled={!file || running} onClick={() => void start()}>
            {error ? t({ id: "common.action.retry", message: "Retry" }) : t({ id: "images.upload.start", message: "Upload" })}
          </Button>
        </>
      }
    >
      <label className="br-field">
        <span className="br-field-label">{t({ id: "images.upload.file", message: "Disk image file" })}</span>
        <input type="file" accept=".iso,.img,.qcow2" onChange={(event) => setFile(event.target.files?.[0])} />
      </label>
      {progress ? <Progress label={t({ id: "images.upload.progress", message: "Upload progress" })} value={progress.loaded} max={progress.total || 1} /> : null}
      {error ? <Alert tone="error">{error}</Alert> : null}
    </Dialog>
  );
}
