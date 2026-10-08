import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Button, Checkbox, Select, TextField } from "@bedrock/design";
import { windowsPreferences } from "../instanceTypes";
import type { Firmware, IsoValues } from "../types";
import { UploadIsoDialog } from "../UploadIsoDialog";
import type { StepProps } from "./commonSteps";

const virtioWin = "virtio-win";
const windowsFirmware: Firmware = { uefi: true, secureBoot: false, tpm: true };

export function IsoStep({ values, set, data, errors }: StepProps<IsoValues>) {
  const { t } = useLingui();
  const [uploading, setUploading] = useState(false);
  const isos = data.images.filter((image) => image.type === "iso" && image.phase === "Ready");
  const windows = values.osPreference.startsWith("windows.");
  return (
    <>
      <Select
        label={t({ id: "wizard.iso", message: "Installation ISO" })}
        value={values.isoName}
        onValueChange={(isoName) => set({ isoName })}
        options={isos.map((image) => ({ value: image.name, label: `${image.name} (${image.scope})` }))}
        {...(errors.isoName ? { error: errors.isoName } : {})}
      />
      <Button variant="secondary" onClick={() => setUploading(true)}>
        {t({ id: "wizard.uploadIso", message: "Upload a new ISO" })}
      </Button>
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
      {uploading ? (
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
