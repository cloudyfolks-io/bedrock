import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Button, Checkbox, Select, TextField } from "@bedrock/design";
import { useCans } from "../../authz/authz";
import { windowsPreferences } from "../instanceTypes";
import type { Firmware, IsoValues } from "../types";
import { UploadIsoDialog } from "../UploadIsoDialog";
import { readyIsos } from "../wizardSteps";
import styles from "../wizard.module.css";
import type { StepProps } from "./commonSteps";

const virtioWin = "virtio-win";
const windowsFirmware: Firmware = { uefi: true, secureBoot: false, tpm: true };

export function IsoStep({ values, set, data, errors }: StepProps<IsoValues>) {
  const { t } = useLingui();
  const [uploading, setUploading] = useState(false);
  const check = { group: "bedrock.cloudyfolks.io", resource: "images", namespace: data.namespace };
  const permissions = useCans([{ ...check, verb: "create" }, { ...check, verb: "create", subresource: "upload" }, { ...check, verb: "get" }]);
  const canUpload = permissions.every((permission) => permission === "allowed");
  const isos = readyIsos(data.images);
  const empty = isos.length === 0;
  const waiting = values.isoName !== "" && !isos.some((image) => image.name === values.isoName);
  const windows = values.osPreference.startsWith("windows.");
  const upload = canUpload ? (
    <Button variant={empty ? "primary" : "secondary"} onClick={() => setUploading(true)}>
      {t({ id: "wizard.uploadIso", message: "Upload a new ISO" })}
    </Button>
  ) : null;
  return (
    <>
      <Select
        label={t({ id: "wizard.iso", message: "Installation ISO" })}
        value={values.isoName}
        onValueChange={(isoName) => set({ isoName })}
        options={isos.map((image) => ({ value: image.name, label: `${image.name} (${image.scope})` }))}
        disabled={empty}
        {...(errors.isoName && !empty ? { error: errors.isoName } : {})}
      />
      {empty || waiting ? (
        <div className={styles.notice}>
          <div role="status">
            <strong>{data.imageStatus === "loading"
              ? t({ id: "wizard.iso.loading", message: "Checking for ready ISO images…" })
              : data.imageStatus === "error"
                ? t({ id: "wizard.iso.loadError", message: "Could not check all ISO images." })
                : empty
                  ? t({ id: "wizard.iso.empty", message: "No ready ISO images" })
                  : t({ id: "wizard.iso.selectionPending", message: "The selected ISO is not ready." })}</strong>
            <p className={styles.note}>{waiting
              ? t({ id: "wizard.iso.waitForReady", message: "Refresh after the platform finishes processing, or select another ready ISO." })
              : canUpload
                ? t({ id: "wizard.iso.uploadHelp", message: "Upload an ISO, then wait for the platform to process it." })
                : permissions.includes("denied")
                  ? t({ id: "wizard.iso.askAdmin", message: "Ask a project administrator to make an ISO available." })
                  : null}</p>
          </div>
          <div className={styles.inlineActions}>
            {upload}
            <Button variant="secondary" disabled={data.imageStatus === "loading"} onClick={data.refreshImages}>
              {t({ id: "wizard.iso.refresh", message: "Refresh images" })}
            </Button>
          </div>
        </div>
      ) : upload}
      {empty && errors.isoName ? <p className={`br-field-error ${styles.note}`} role="alert" tabIndex={-1} aria-invalid="true">{errors.isoName}</p> : null}
      <TextField label={t({ id: "wizard.osPreference", message: "Operating system preference" })} name="os-preference" dir="ltr" autoComplete="off" value={values.osPreference} onChange={(osPreference) => set({ osPreference })} />
      <div className="br-chips">
        {windowsPreferences.map((preference) => (
          <Button key={preference} variant="secondary" onClick={() => set({ osPreference: preference, firmware: windowsFirmware })}>
            {preference}
          </Button>
        ))}
      </div>
      {windows ? (
        <Checkbox
          label={t({ id: "wizard.virtioWin", message: "Attach the virtio-win drivers" })}
          checked={values.extraCdroms.includes(virtioWin)}
          onCheckedChange={(on) => set({ extraCdroms: on ? [...values.extraCdroms.filter((name) => name !== virtioWin), virtioWin] : values.extraCdroms.filter((name) => name !== virtioWin) })}
        />
      ) : null}
      {uploading && canUpload ? (
        <UploadIsoDialog
          namespace={data.namespace}
          onClose={() => setUploading(false)}
          onUploaded={(isoName) => {
            set({ isoName });
            setUploading(false);
          }}
        />
      ) : null}
    </>
  );
}
