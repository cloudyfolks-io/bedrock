import { useLingui } from "@lingui/react/macro";
import { Button, Checkbox, NumberField, Select, Switch, TextField } from "@bedrock/design";
import type { WizardData } from "../useWizardData";
import { volumeProblem } from "../wizardSteps";
import styles from "../wizard.module.css";

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
      {extra ? <ExistingVolumes data={data} {...extra} error={errors.extraVolumes} /> : null}
    </>
  );
}

function ExistingVolumes({ data, selected, onChange, error }: {
  data: WizardData;
  selected: string[];
  onChange: (volumes: string[]) => void;
  error: string | undefined;
}) {
  const { t, i18n } = useLingui();
  const { volumes, volumeStatus, refreshVolumes } = data;
  const names = [...new Set([...(volumes ?? []).map(({ name }) => name), ...selected])];
  return (
    <fieldset className="br-form-object">
      <legend>{t({ id: "wizard.extraVolumes", message: "Attach existing volumes" })}</legend>
      {volumeStatus === "loading" ? (
        <p className={styles.note} role="status">{t({ id: "wizard.volumes.loading", message: "Loading project volumes…" })}</p>
      ) : volumeStatus !== "ready" ? (
        <div className={styles.inlineActions}>
          <p className={styles.note} role="status">{volumeStatus === "vm-error"
            ? t({ id: "wizard.volumes.claimsError", message: "Could not verify which volumes are referenced by virtual machines." })
            : t({ id: "wizard.volumes.error", message: "Could not load project volumes." })}</p>
          <Button variant="secondary" onClick={() => void refreshVolumes()}>{t({ id: "common.action.retry", message: "Retry" })}</Button>
        </div>
      ) : volumes?.length === 0 ? (
        <p className={styles.note}>{t({ id: "wizard.volumes.empty", message: "No existing volumes in this project." })}</p>
      ) : null}
      {error ? <p className={`br-field-error ${styles.note}`} role="alert" tabIndex={-1} aria-invalid="true">{error}</p> : null}
      {names.map((name) => {
        const problem = volumes && volumeProblem(volumes, name);
        const checked = selected.includes(name);
        const unavailable = !volumes || problem !== undefined;
        return (
          <div className={styles.inlineActions} key={name}>
            <Checkbox
              label={problem ? `${name} · ${i18n._(problem)}` : name}
              checked={checked}
              disabled={unavailable}
              onCheckedChange={(on) => {
                if (!unavailable) onChange(on ? withValue(selected, name) : withoutValue(selected, name));
              }}
            />
            {checked && unavailable ? (
              <Button variant="secondary" onClick={() => onChange(withoutValue(selected, name))}>
                {t({ id: "wizard.volume.removeAction", message: `Remove ${name}` })}
              </Button>
            ) : null}
          </div>
        );
      })}
    </fieldset>
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
