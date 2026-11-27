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
import { buildIsoVirtualMachine } from "./buildVirtualMachine";
import { BasicsStep, DisksStep, NetworkStep, SizeStep, type Common, type StepProps } from "./steps/commonSteps";
import { FirmwareStep } from "./steps/FirmwareStep";
import { IsoStep } from "./steps/IsoStep";
import { ReviewStep } from "./steps/ReviewStep";
import type { IsoStep as IsoStepId, IsoValues } from "./types";
import { useWizardData } from "./useWizardData";
import { useWizardNavigation, WizardFrame, type StepInfo } from "./WizardFrame";
import { hasChanges, isoStepProblems, isoSteps, readyIsos, stepOfField } from "./wizardSteps";

const labels: Record<IsoStepId, StepInfo["label"]> = {
  basics: msg({ id: "wizard.step.basics", message: "Basics" }),
  iso: msg({ id: "wizard.step.iso", message: "ISO" }),
  size: msg({ id: "wizard.step.size", message: "Size" }),
  firmware: msg({ id: "wizard.step.firmware", message: "Firmware" }),
  disks: msg({ id: "wizard.step.disks", message: "Disks" }),
  network: msg({ id: "wizard.step.network", message: "Network" }),
  review: msg({ id: "wizard.step.review", message: "Review" }),
};

const initial: IsoValues = {
  name: "",
  isoName: "",
  extraCdroms: [],
  osPreference: "",
  instanceType: "",
  customSize: false,
  cores: null,
  memoryMiB: null,
  rootDiskGi: 40,
  storageClass: "",
  firmware: { uefi: true, secureBoot: false, tpm: false },
  subnetRef: "",
  securityGroups: [],
  running: false,
};

export default function IsoWizard() {
  const projectName = useCurrentProjectName() ?? "";
  return <IsoWizardForm key={projectName} projectName={projectName} />;
}

function IsoWizardForm({ projectName }: { projectName: string }) {
  const { t, i18n } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const def = definitionFor("virtualmachines");
  const data = useWizardData();
  const canCreate = useCan({ verb: "create", group: "bedrock.cloudyfolks.io", resource: "virtualmachines", namespace: data.namespace });
  const form = useForm({ defaultValues: initial });
  const values = useStore(form.store, (state) => state.values);
  const [index, setIndex] = useState(0);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [prefilledSubnet, setPrefilledSubnet] = useState("");
  const step = isoSteps[index] ?? "basics";
  const navigation = useWizardNavigation(hasChanges(values, { ...initial, subnetRef: prefilledSubnet }));
  const environment = { vmNames: data.vmNames, images: data.images, subnets: data.subnets };

  useEffect(() => {
    if (data.defaultSubnet !== "" && form.getFieldValue("subnetRef") === "") {
      form.setFieldValue("subnetRef", data.defaultSubnet);
      setPrefilledSubnet(data.defaultSubnet);
    }
  }, [data.defaultSubnet, form]);

  const set = (patch: Partial<IsoValues>) => Object.entries(patch).forEach(([key, value]) => form.setFieldValue(key as never, value as never));

  function next() {
    const problems = isoStepProblems(step, values, environment);
    setErrors(Object.fromEntries(problems.map((problem) => [problem.field, i18n._(problem.message)])));
    if (problems.length === 0) {
      setIndex((current) => Math.min(isoSteps.length - 1, current + 1));
    }
  }

  async function submit() {
    if (!def || canCreate !== "allowed" || busy) {
      return;
    }
    const [problem] = isoStepProblems("iso", values, environment);
    if (problem) {
      setErrors({ [problem.field]: i18n._(problem.message) });
      setIndex(isoSteps.indexOf("iso"));
      return;
    }
    setBusy(true);
    try {
      await kubeClient(http).create(refOf(def), data.namespace, buildIsoVirtualMachine(values, { namespace: data.namespace }));
      toast.show({ tone: "success", title: t({ id: "action.accepted", message: "Request accepted" }) });
      navigation.complete(`/p/${projectName}/vms/${values.name}?tab=console`);
    } catch (error) {
      if (isKubeStatusError(error) && error.reason === "AlreadyExists") {
        setErrors({ name: t({ id: "forms.name.exists", message: "An object with this name already exists" }) });
        setIndex(0);
      } else if (isKubeStatusError(error) && error.causes.length > 0) {
        const step = stepOfField(error.causes[0]?.field ?? "") as IsoStepId;
        const target = isoSteps.indexOf(step);
        setErrors({ [step === "iso" ? "isoName" : "rootDiskGi"]: error.causes[0]?.message ?? "" });
        setIndex(target >= 0 ? target : index);
      } else {
        toast.show({ tone: "danger", title: t({ id: "action.error.failed", message: "The action failed" }), description: error instanceof Error ? error.message : "" });
      }
    } finally {
      setBusy(false);
    }
  }

  if (!data.ready || canCreate === "pending") {
    return <Spinner />;
  }
  if (canCreate === "denied") {
    return <EmptyState title={t({ id: "wizard.noAccess", message: "You cannot create virtual machines here" })} />;
  }

  const props: StepProps<Common> = { values, set, data, errors };
  const content =
    step === "basics" ? (
      <BasicsStep {...props} />
    ) : step === "iso" ? (
      <IsoStep values={values} set={set} data={data} errors={errors} />
    ) : step === "size" ? (
      <SizeStep {...props} />
    ) : step === "firmware" ? (
      <FirmwareStep values={values} set={set} data={data} errors={errors} />
    ) : step === "disks" ? (
      <DisksStep {...props} />
    ) : step === "network" ? (
      <NetworkStep {...props} />
    ) : (
      <ReviewStep values={values} object={buildIsoVirtualMachine(values, { namespace: data.namespace })} />
    );

  return (
    <WizardFrame
      title={t({ id: "wizard.iso.title", message: "Install a virtual machine from an ISO" })}
      steps={isoSteps.map((id) => ({ id, label: labels[id] }))}
      index={index}
      blocker={navigation.blocker}
      busy={busy}
      nextDisabled={step === "iso" && readyIsos(data.images).length === 0}
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
