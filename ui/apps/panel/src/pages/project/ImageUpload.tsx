import { useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Dialog, Progress, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { listKey } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { definitionFor, refOf } from "../../resources/registry";
import { useSession } from "../../api/session";
import { isUploadUrl, requestUploadToken, uploadFile } from "./imageUploadApi";

export function ImageUpload({ image, onClose, onUploaded }: { image: KubeObject; onClose: () => void; onUploaded?: () => void }) {
  const { t } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const queryClient = useQueryClient();
  const def = definitionFor("images");
  const host = useSession().data?.host ?? "";
  const [file, setFile] = useState<File | undefined>();
  const [progress, setProgress] = useState<{ loaded: number; total: number } | undefined>();
  const [error, setError] = useState<string | undefined>();
  const [running, setRunning] = useState(false);
  const controller = useRef<AbortController | undefined>(undefined);

  function cancel() {
    controller.current?.abort();
    controller.current = undefined;
    setProgress(undefined);
    setRunning(false);
  }

  async function start() {
    if (controller.current || !file || !image.metadata.namespace) {
      return;
    }
    setError(undefined);
    setProgress(undefined);
    setRunning(true);
    const abort = new AbortController();
    controller.current = abort;
    try {
      const grant = await requestUploadToken(http, image.metadata.namespace, image.metadata.name, abort.signal);
      if (abort.signal.aborted) return;
      if (!isUploadUrl(grant.url, host)) {
        setError(t({ id: "images.upload.badUrl", message: "The upload address is not on this platform. The upload was stopped." }));
        return;
      }
      setProgress({ loaded: 0, total: file.size });
      await uploadFile(grant.url, grant.token, file, (loaded, total) => {
        if (!abort.signal.aborted) setProgress({ loaded, total });
      }, abort.signal);
      if (abort.signal.aborted) return;
      toast.show({
        tone: "success",
        title: t({ id: "images.upload.transferred", message: "File transferred" }),
        description: t({ id: "images.upload.processing", message: "The platform still needs to process the image." }),
      });
      if (def) {
        void queryClient.invalidateQueries({ queryKey: listKey(refOf(def), image.metadata.namespace, {}) });
      }
      onUploaded?.();
      onClose();
    } catch (reason) {
      if (!abort.signal.aborted && !(reason instanceof DOMException && reason.name === "AbortError")) {
        setError(reason instanceof Error ? reason.message : t({ id: "action.error.failed", message: "The action failed" }));
      }
    } finally {
      if (controller.current === abort) {
        controller.current = undefined;
        setRunning(false);
      }
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) {
          cancel();
          onClose();
        }
      }}
      title={t({ id: "images.upload.title", message: `Upload ${image.metadata.name}` })}
      actions={
        <>
          {running ? (
            <Button variant="secondary" onClick={cancel}>
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
        <input type="file" accept=".iso,.img,.qcow2" disabled={running} onChange={(event) => setFile(event.target.files?.[0])} />
      </label>
      {running && !progress ? <p role="status">{t({ id: "wizard.uploadIso.waiting", message: "Preparing the upload" })}</p> : null}
      {progress ? <Progress label={t({ id: "images.upload.progress", message: "Upload progress" })} value={progress.loaded} max={progress.total || 1} /> : null}
      {error ? <Alert tone="error">{error}</Alert> : null}
    </Dialog>
  );
}
