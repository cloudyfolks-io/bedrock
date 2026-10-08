import { useEffect, useState } from "react";
import { useNavigate } from "react-router";
import { useForm, useStore } from "@tanstack/react-form";
import { useQueryClient } from "@tanstack/react-query";
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
import { WizardFrame, type StepInfo } from "./WizardFrame";
import { hasChanges, isoStepProblems, isoSteps, stepOfField } from "./wizardSteps";

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
  rootDiskGi: 40,
  storageClass: "",
  firmware: { uefi: true, secureBoot: false, tpm: false },
  subnetRef: "",
  securityGroups: [],
  running: false,
};

export default function IsoWizard() {
  const { t, i18n } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const def = definitionFor("virtualmachines");
  const data = useWizardData();
  const projectName = useCurrentProjectName() ?? "";
  const canCreate = useCan({ verb: "create", group: "bedrock.cloudyfolks.io", resource: "virtualmachines", namespace: data.namespace });
  const form = useForm({ defaultValues: initial });
  const values = useStore(form.store, (state) => state.values);
  const [index, setIndex] = useState(0);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const step = isoSteps[index] ?? "basics";

  useEffect(() => {
    if (data.defaultSubnet !== "" && form.getFieldValue("subnetRef") === "") {
      form.setFieldValue("subnetRef", data.defaultSubnet);
    }
  }, [data.defaultSubnet, form]);

  const set = (patch: Partial<IsoValues>) => Object.entries(patch).forEach(([key, value]) => form.setFieldValue(key as never, value as never));

  function next() {
    const problems = isoStepProblems(step, values, { vmNames: data.vmNames, images: data.images, subnets: data.subnets });
    setErrors(Object.fromEntries(problems.map((problem) => [problem.field, i18n._(problem.message)])));
    if (problems.length === 0) {
      setIndex((current) => Math.min(isoSteps.length - 1, current + 1));
    }
  }

  async function submit() {
    if (!def) {
      return;
    }
    const { name } = values;
    setBusy(true);
    try {
      await kubeClient(http).create(refOf(def), data.namespace, buildIsoVirtualMachine(values, { namespace: data.namespace }));
      toast.show({ tone: "success", title: t({ id: "forms.create.done", message: `Created ${name}` }) });
      void queryClient.invalidateQueries({ queryKey: ["kube", def.group, def.plural] });
      form.reset();
      void navigate(`/p/${projectName}/vms/${values.name}?tab=console`);
    } catch (error) {
      if (isKubeStatusError(error) && error.reason === "AlreadyExists") {
        setErrors({ name: t({ id: "forms.name.exists", message: "An object with this name already exists" }) });
        setIndex(0);
      } else if (isKubeStatusError(error) && error.causes.length > 0) {
        const target = isoSteps.indexOf(stepOfField(error.causes[0]?.field ?? "") as IsoStepId);
        setErrors({ rootDiskGi: error.causes[0]?.message ?? "" });
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
      <ReviewStep object={buildIsoVirtualMachine(values, { namespace: data.namespace })} />
    );

  return (
    <WizardFrame
      title={t({ id: "wizard.iso.title", message: "Install a virtual machine from an ISO" })}
      steps={isoSteps.map((id) => ({ id, label: labels[id] }))}
      index={index}
      dirty={hasChanges(values, { ...initial, subnetRef: data.defaultSubnet })}
      busy={busy}
      leaveTo={`/p/${projectName}/vms`}
      onBack={() => setIndex((current) => Math.max(0, current - 1))}
      onNext={next}
      onSubmit={() => void submit()}
    >
      {content}
    </WizardFrame>
  );
}
