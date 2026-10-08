import { useLingui } from "@lingui/react/macro";
import { Checkbox, CodeTextarea } from "@bedrock/design";
import type { DiskValues } from "../types";
import type { StepProps } from "./commonSteps";

export function AccessStep({ values, set, data, errors }: StepProps<DiskValues>) {
  const { t } = useLingui();
  return (
    <>
      <fieldset className="br-form-object">
        <legend>{t({ id: "nav.item.sshKeys", message: "SSH keys" })}</legend>
        {data.sshKeys.map((key) => (
          <Checkbox
            key={key}
            label={key}
            checked={values.sshKeys.includes(key)}
            onCheckedChange={(on) => set({ sshKeys: on ? [...values.sshKeys.filter((entry) => entry !== key), key] : values.sshKeys.filter((entry) => entry !== key) })}
          />
        ))}
      </fieldset>
      <CodeTextarea label={t({ id: "wizard.userData", message: "Cloud-init user data" })} value={values.userData} onChange={(userData) => set({ userData })} rows={8} {...(errors.userData ? { error: errors.userData } : {})} />
    </>
  );
}
