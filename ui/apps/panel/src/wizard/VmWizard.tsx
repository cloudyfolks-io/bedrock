import { useEffect, useState } from "react";
import { useForm, useStore } from "@tanstack/react-form";
import { useLingui } from "@lingui/react/macro";
import { msg } from "@lingui/core/macro";
import { useToast } from "@bedrock/design";
import { useHttp } from "../api/HttpProvider";
import { isKubeStatusError } from "../api/http";
import { kubeClient } from "../api/kube";
import { useCan } from "../authz/authz";
import { EmptyState } from "../components/EmptyState";
import { Spinner } from "../components/Spinner";
import { definitionFor, refOf } from "../resources/registry";
import { useCurrentProjectName } from "../shell/context";
import { buildDiskVirtualMachine } from "./buildVirtualMachine";
import { AccessStep } from "./steps/AccessStep";
import { BasicsStep, DisksStep, NetworkStep, SizeStep, type Common, type StepProps } from "./steps/commonSteps";
import { ImageStep } from "./steps/ImageStep";
import { ReviewStep } from "./steps/ReviewStep";
import type { DiskStep, DiskValues } from "./types";
import { useWizardData } from "./useWizardData";
import { useWizardNavigation, WizardFrame, type StepInfo } from "./WizardFrame";
import { diskStepProblems, diskSteps, extraVolumeProblem, hasChanges, stepOfField } from "./wizardSteps";

const labels: Record<DiskStep, StepInfo["label"]> = {
  basics: msg({ id: "wizard.step.basics", message: "Basics" }),
  image: msg({ id: "wizard.step.image", message: "Image" }),
  size: msg({ id: "wizard.step.size", message: "Size" }),
  disks: msg({ id: "wizard.step.disks", message: "Disks" }),
  network: msg({ id: "wizard.step.network", message: "Network" }),
  access: msg({ id: "wizard.step.access", message: "Access" }),
  review: msg({ id: "wizard.step.review", message: "Review" }),
};

const initial: DiskValues = {
  name: "",
  imageName: "",
  imageScope: "cluster",
  instanceType: "",
  customSize: false,
  cores: null,
  memoryMiB: null,
  preference: "",
  rootDiskGi: 20,
  storageClass: "",
  extraVolumes: [],
  subnetRef: "",
  securityGroups: [],
  sshKeys: [],
  userData: "",
  running: true,
};

export default function VmWizard() {
  const projectName = useCurrentProjectName() ?? "";
  return <VmWizardForm key={projectName} projectName={projectName} />;
}

function VmWizardForm({ projectName }: { projectName: string }) {
  const { t, i18n } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const def = definitionFor("virtualmachines");
  const data = useWizardData({ loadVolumes: true });
  const canCreate = useCan({ verb: "create", group: "bedrock.cloudyfolks.io", resource: "virtualmachines", namespace: data.namespace });
  const form = useForm({ defaultValues: initial });
  const values = useStore(form.store, (state) => state.values);
  const [index, setIndex] = useState(0);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [prefilledSubnet, setPrefilledSubnet] = useState("");
  const step = diskSteps[index] ?? "basics";
  const navigation = useWizardNavigation(hasChanges(values, { ...initial, subnetRef: prefilledSubnet }));

  useEffect(() => {
    if (data.defaultSubnet !== "" && form.getFieldValue("subnetRef") === "") {
      form.setFieldValue("subnetRef", data.defaultSubnet);
      setPrefilledSubnet(data.defaultSubnet);
    }
  }, [data.defaultSubnet, form]);

  const set = (patch: Partial<DiskValues>) => Object.entries(patch).forEach(([key, value]) => form.setFieldValue(key as never, value as never));
  const environment = { vmNames: data.vmNames, images: data.images, subnets: data.subnets };

  function validate(current: DiskStep): boolean {
    const problems = [
      ...diskStepProblems(current, values, environment),
      ...(current === "disks" ? extraVolumeProblem(values.extraVolumes, data.volumes) : []),
    ];
    setErrors(Object.fromEntries(problems.map((problem) => [problem.field, i18n._(problem.message)])));
    return problems.length === 0;
  }

  function next() {
    if (validate(step)) {
      setIndex((current) => Math.min(diskSteps.length - 1, current + 1));
    }
  }

  async function submit() {
    if (!def || canCreate !== "allowed" || busy) {
      return;
    }
    const object = buildDiskVirtualMachine(values, { namespace: data.namespace });
    setBusy(true);
    try {
      if (values.extraVolumes.length > 0) {
        const latest = await data.refreshVolumes();
        if (!navigation.active.current) return;
        const [problem] = extraVolumeProblem(values.extraVolumes, latest);
        if (problem) {
          setErrors({ [problem.field]: i18n._(problem.message) });
          setIndex(diskSteps.indexOf("disks"));
          return;
        }
      }
      await kubeClient(http).create(refOf(def), data.namespace, object);
      toast.show({ tone: "success", title: t({ id: "action.accepted", message: "Request accepted" }) });
      navigation.complete(`/p/${projectName}/vms/${values.name}`);
    } catch (error) {
      if (isKubeStatusError(error) && error.reason === "AlreadyExists") {
        setErrors({ name: t({ id: "forms.name.exists", message: "An object with this name already exists" }) });
        setIndex(0);
      } else if (isKubeStatusError(error) && error.causes.length > 0) {
        const cause = error.causes[0];
        const target = diskSteps.indexOf(stepOfField(cause?.field ?? "") as DiskStep);
        setErrors({ [fieldOfCause(cause?.field ?? "")]: cause?.message ?? "" });
        setIndex(target >= 0 ? target : index);
      } else {
        toast.show({ tone: "danger", title: t({ id: "action.error.failed", message: "The action failed" }), description: error instanceof Error ? error.message : "" });
      }
    } finally {
      setBusy(false);
    }
  }

  if (!data.ready) {
    return <Spinner />;
  }
  if (canCreate === "pending") {
    return <Spinner />;
  }
  if (canCreate === "denied") {
    return <EmptyState title={t({ id: "wizard.noAccess", message: "You cannot create virtual machines here" })} />;
  }
  const props: StepProps<Common> = { values, set, data, errors };
  const content =
    step === "basics" ? (
      <BasicsStep {...props} />
    ) : step === "image" ? (
      <ImageStep values={values} set={set} data={data} errors={errors} />
    ) : step === "size" ? (
      <SizeStep {...props} />
    ) : step === "disks" ? (
      <DisksStep {...props} extra={{ selected: values.extraVolumes, onChange: (extraVolumes) => set({ extraVolumes }) }} />
    ) : step === "network" ? (
      <NetworkStep {...props} />
    ) : step === "access" ? (
      <AccessStep values={values} set={set} data={data} errors={errors} />
    ) : (
      <ReviewStep values={values} object={buildDiskVirtualMachine(values, { namespace: data.namespace })} />
    );

  return (
    <WizardFrame
      title={t({ id: "wizard.title", message: "Create a virtual machine" })}
      steps={diskSteps.map((id) => ({ id, label: labels[id] }))}
      index={index}
      blocker={navigation.blocker}
      busy={busy}
      errors={errors}
      leaveTo={`/p/${projectName}/vms`}
      onBack={() => setIndex((current) => Math.max(0, current - 1))}
      onNext={next}
      onSubmit={() => void submit()}
    >
      {content}
    </WizardFrame>
  );
}

function fieldOfCause(field: string): string {
  const table: [string, string][] = [
    ["spec.rootDisk", "rootDiskGi"],
    ["spec.disks", "extraVolumes"],
    ["spec.instanceType", "instanceType"],
    ["spec.compute.cores", "cores"],
    ["spec.compute.memoryMiB", "memoryMiB"],
    ["spec.image", "imageName"],
    ["spec.networks", "subnetRef"],
    ["spec.cloudInit", "userData"],
    ["metadata.name", "name"],
  ];
  return table.find(([prefix]) => field.startsWith(prefix))?.[1] ?? "name";
}
