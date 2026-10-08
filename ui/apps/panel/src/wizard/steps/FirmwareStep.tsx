import { useLingui } from "@lingui/react/macro";
import { Switch } from "@bedrock/design";
import type { IsoValues } from "../types";
import type { StepProps } from "./commonSteps";

export function FirmwareStep({ values, set }: StepProps<IsoValues>) {
  const { t } = useLingui();
  const change = (patch: Partial<IsoValues["firmware"]>) => set({ firmware: { ...values.firmware, ...patch } });
  return (
    <>
      <Switch label="UEFI" checked={values.firmware.uefi} onCheckedChange={(uefi) => change({ uefi })} />
      <Switch label={t({ id: "wizard.secureBoot", message: "Secure boot" })} checked={values.firmware.secureBoot} onCheckedChange={(secureBoot) => change({ secureBoot })} />
      <Switch label="TPM" checked={values.firmware.tpm} onCheckedChange={(tpm) => change({ tpm })} />
    </>
  );
}
