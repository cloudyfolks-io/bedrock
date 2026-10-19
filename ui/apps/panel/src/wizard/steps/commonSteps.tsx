import { useLingui } from "@lingui/react/macro";
import { Checkbox, NumberField, Select, Switch, TextField } from "@bedrock/design";
import type { WizardData } from "../useWizardData";

export interface Common {
  name: string;
  running: boolean;
  instanceType: string;
  customSize: boolean;
  cores: number | null;
  memoryMiB: number | null;
  rootDiskGi: number | null;
  storageClass: string;
  subnetRef: string;
  securityGroups: string[];
}

export interface StepProps<V> {
  values: V;
  set: (patch: Partial<V>) => void;
  data: WizardData;
  errors: Record<string, string>;
}

function withValue(list: readonly string[], value: string): string[] {
  return [...list.filter((entry) => entry !== value), value];
}

function withoutValue(list: readonly string[], value: string): string[] {
  return list.filter((entry) => entry !== value);
}

export function BasicsStep({ values, set, errors }: StepProps<Common>) {
  const { t } = useLingui();
  return (
    <>
      <TextField label={t({ id: "forms.name.label", message: "Name" })} name="vm-name" autoComplete="off" dir="ltr" value={values.name} onChange={(name) => set({ name })} {...(errors.name ? { error: errors.name } : {})} />
      <Switch label={t({ id: "wizard.startAfter", message: "Start after creation" })} checked={values.running} onCheckedChange={(running) => set({ running })} />
    </>
  );
}

export function SizeStep({ values, set, data, errors }: StepProps<Common>) {
  const { t } = useLingui();
  return (
    <>
      <Switch label={t({ id: "wizard.customSize", message: "Custom size" })} checked={values.customSize} onCheckedChange={(customSize) => set({ customSize })} />
      {values.customSize ? (
        <>
          <NumberField label={t({ id: "wizard.cores", message: "CPU cores" })} value={values.cores} onValueChange={(cores) => set({ cores })} min={1} {...(errors.cores ? { error: errors.cores } : {})} />
          <NumberField label={t({ id: "wizard.memoryMiB", message: "Memory (MiB)" })} value={values.memoryMiB} onValueChange={(memoryMiB) => set({ memoryMiB })} min={128} {...(errors.memoryMiB ? { error: errors.memoryMiB } : {})} />
        </>
      ) : (
        <Select
          label={t({ id: "col.instanceType", message: "Instance type" })}
          value={values.instanceType}
          onValueChange={(instanceType) => set({ instanceType })}
          options={data.instanceTypes.map((type) => ({ value: type.name, label: `${type.name} · ${type.vcpu} vCPU · ${type.memoryGiB} GiB` }))}
          {...(errors.instanceType ? { error: errors.instanceType } : {})}
        />
      )}
    </>
  );
}

export function DisksStep({ values, set, data, errors, extra }: StepProps<Common> & { extra?: { selected: string[]; onChange: (volumes: string[]) => void } }) {
  const { t } = useLingui();
  return (
    <>
      <NumberField label={t({ id: "wizard.rootDisk", message: "Root disk size (GiB)" })} value={values.rootDiskGi} onValueChange={(rootDiskGi) => set({ rootDiskGi })} min={1} {...(errors.rootDiskGi ? { error: errors.rootDiskGi } : {})} />
      <TextField label={t({ id: "col.storageClass", message: "Storage class" })} name="storage-class" dir="ltr" autoComplete="off" value={values.storageClass} onChange={(storageClass) => set({ storageClass })} />
      {extra ? (
        <fieldset className="br-form-object">
          <legend>{t({ id: "wizard.extraVolumes", message: "Attach existing volumes" })}</legend>
          {data.volumes.map((volume) => (
            <Checkbox key={volume} label={volume} checked={extra.selected.includes(volume)} onCheckedChange={(on) => extra.onChange(on ? withValue(extra.selected, volume) : withoutValue(extra.selected, volume))} />
          ))}
        </fieldset>
      ) : null}
    </>
  );
}

export function NetworkStep({ values, set, data, errors }: StepProps<Common>) {
  const { t } = useLingui();
  return (
    <>
      <Select
        label={t({ id: "col.subnet", message: "Subnet" })}
        value={values.subnetRef}
        onValueChange={(subnetRef) => set({ subnetRef })}
        options={data.subnets.map((subnet) => ({ value: subnet, label: subnet }))}
        {...(errors.subnetRef ? { error: errors.subnetRef } : {})}
      />
      <fieldset className="br-form-object">
        <legend>{t({ id: "vmDetail.securityGroups", message: "Security groups" })}</legend>
        {data.securityGroups.map((group) => (
          <Checkbox key={group} label={group} checked={values.securityGroups.includes(group)} onCheckedChange={(on) => set({ securityGroups: on ? withValue(values.securityGroups, group) : withoutValue(values.securityGroups, group) })} />
        ))}
      </fieldset>
    </>
  );
}
