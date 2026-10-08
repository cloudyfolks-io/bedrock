import { useLingui } from "@lingui/react/macro";
import { Select } from "@bedrock/design";
import type { DiskValues, WizardImage } from "../types";
import type { StepProps } from "./commonSteps";

export function ImageStep({ values, set, data, errors }: StepProps<DiskValues>) {
  const { t } = useLingui();
  const disks = data.images.filter((image) => image.type === "disk" && image.phase === "Ready");
  const key = (image: WizardImage) => `${image.scope}/${image.name}`;
  return (
    <Select
      label={t({ id: "wizard.image", message: "Image" })}
      value={values.imageName === "" ? "" : `${values.imageScope}/${values.imageName}`}
      onValueChange={(next) => {
        const chosen = disks.find((image) => key(image) === next);
        if (chosen) {
          set({ imageName: chosen.name, imageScope: chosen.scope, preference: chosen.preference, rootDiskGi: Math.max(values.rootDiskGi ?? 0, chosen.sizeGi) });
        }
      }}
      options={disks.map((image) => ({ value: key(image), label: `${image.name} (${image.scope})` }))}
      {...(errors.imageName ? { error: errors.imageName } : {})}
    />
  );
}
