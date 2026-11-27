import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Button, Dialog, Select, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { kubeClient } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { listAt, textAt } from "../../resources/path";
import { definitionFor, refOf } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { useCurrentProject } from "../../shell/context";
import { useLiveList } from "../../stream/live";
import styles from "./elasticIps.module.css";

function required(plural: string) {
  const def = definitionFor(plural);
  if (!def) {
    throw new Error(`missing definition ${plural}`);
  }
  return def;
}

function useTargetPatch(): (eip: KubeObject, target: unknown) => Promise<boolean> {
  const { t } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const def = required("elasticips");
  return async (eip, target) => {
    const { namespace, name, resourceVersion } = eip.metadata;
    if (!namespace) return false;
    try {
      await kubeClient(http).mergePatch(refOf(def), namespace, name, { ...(resourceVersion ? { metadata: { resourceVersion } } : {}), spec: { target } });
      return true;
    } catch (error) {
      toast.show({ tone: "danger", title: t({ id: "action.error.failed", message: "The action failed" }), description: error instanceof Error ? error.message : "" });
      return false;
    }
  };
}

function compatibleSubnets(vm: KubeObject, eip: KubeObject, ips: KubeObject[]): string[] {
  const networks = listAt(vm, "spec.networks") as { subnetRef?: string; elasticIPRef?: string }[];
  const claimed = ips.filter((ip) => ip.metadata.name !== eip.metadata.name && textAt(ip, "spec.target.kind") === "VirtualMachine" && textAt(ip, "spec.target.name") === vm.metadata.name)
    .map((ip) => textAt(ip, "spec.target.subnetRef") || networks[0]?.subnetRef);
  return networks.flatMap(({ subnetRef, elasticIPRef }, index) =>
    subnetRef && (!elasticIPRef || elasticIPRef === eip.metadata.name) && !claimed.includes(subnetRef) && networks.findIndex((nic) => nic.subnetRef === subnetRef) === index ? [subnetRef] : [],
  );
}

function AttachDialog({ eip, namespace, onClose }: { eip: KubeObject; namespace: string; onClose: () => void }) {
  const { t } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const vmRef = refOf(required("virtualmachines"));
  const lbRef = refOf(required("loadbalancers"));
  const ips = useLiveList(refOf(required("elasticips")), { namespace });
  const vms = useLiveList(vmRef, { namespace });
  const balancers = useLiveList(lbRef, { namespace });
  const [kind, setKind] = useState("VirtualMachine");
  const [name, setName] = useState("");
  const [subnet, setSubnet] = useState("");
  const [busy, setBusy] = useState(false);
  const patch = useTargetPatch();
  const isVm = kind === "VirtualMachine";
  const targets = isVm ? vms : balancers;
  function compatibleTarget(item: KubeObject) {
    if (item.metadata.namespace !== namespace || item.metadata.deletionTimestamp) return undefined;
    if (!isVm) return textAt(item, "spec.elasticIPRef") === eip.metadata.name ? { name: item.metadata.name, subnets: [] } : undefined;
    const subnets = compatibleSubnets(item, eip, ips.data?.items ?? []);
    return subnets.length > 0 ? { name: item.metadata.name, subnets } : undefined;
  }
  const items = (targets.isSuccess ? targets.data.items : []).map(compatibleTarget).filter((item) => item !== undefined);
  const current = ips.isSuccess ? ips.data.items.find((item) => item.metadata.namespace === namespace && item.metadata.name === eip.metadata.name && item.metadata.uid === eip.metadata.uid) : undefined;
  const canAttach = current !== undefined && !current.metadata.deletionTimestamp && current.spec?.target == null;
  const selected = items.find((item) => item.name === name);
  const subnets = selected?.subnets ?? [];
  const ready = canAttach && selected !== undefined && (!isVm || subnets.includes(subnet));
  const targetError = t({ id: "eip.attach.targets.error", message: "Targets could not be loaded. Check your access and try again." });
  const emptyMessage = targets.isPending
    ? t({ id: "eip.attach.targets.loading", message: "Loading targets…" })
    : targets.isError
      ? targetError
      : isVm
        ? t({ id: "eip.attach.vm.empty", message: "No compatible virtual machine network interfaces are available." })
        : t({ id: "eip.attach.lb.empty", message: "No load balancer in this project references this Elastic IP. Set its Elastic IP field first." });

  async function attach() {
    if (!current || !ready || busy) return;
    setBusy(true);
    try {
      // The list watch can pause between selection and submission.
      const target = compatibleTarget(await kubeClient(http).get(isVm ? vmRef : lbRef, namespace, name));
      if (target?.name === name && (!isVm || target.subnets.includes(subnet))) {
        if (await patch(current, { kind, name, ...(isVm ? { subnetRef: subnet } : {}) })) onClose();
        return;
      }
      void targets.refetch();
    } catch {
      // A failed GET does not authorize a write based on the list snapshot.
    } finally {
      setBusy(false);
    }
    setName("");
    setSubnet("");
    toast.show({ tone: "danger", title: t({ id: "action.error.failed", message: "The action failed" }), description: targetError });
  }

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) {
          onClose();
        }
      }}
      title={t({ id: "eip.attach.title", message: `Attach ${eip.metadata.name}` })}
      actions={
        <Button variant="primary" disabled={!ready || busy} onClick={() => void attach()}>
          {t({ id: "eip.attach.confirm", message: "Attach" })}
        </Button>
      }
    >
      <div className={styles.fields}>
        {!ips.isPending && !canAttach ? <p role="status">{t({ id: "eip.attach.unavailable", message: "This Elastic IP is no longer available to attach." })}</p> : null}
        <Select
          label={t({ id: "eip.attach.kind", message: "Target type" })}
          value={kind}
          onValueChange={(next) => {
            setKind(next);
            setName("");
            setSubnet("");
          }}
          options={[
            { value: "VirtualMachine", label: t({ id: "eip.attach.vm", message: "Virtual machine network interface" }) },
            { value: "LoadBalancer", label: t({ id: "eip.attach.lb", message: "Load balancer" }) },
          ]}
        />
        <div className={styles.fields}>
          {items.length === 0 ? <p role="status">{emptyMessage}</p> : (
            <Select label={t({ id: "eip.attach.target", message: "Target" })} value={selected?.name ?? ""} onValueChange={(next) => { setName(next); setSubnet(""); }} placeholder="—" options={items.map((item) => ({ value: item.name, label: item.name }))} />
          )}
          {isVm && selected ? (
            <Select label={t({ id: "col.subnet", message: "Subnet" })} value={subnets.includes(subnet) ? subnet : ""} onValueChange={setSubnet} placeholder="—" options={subnets.map((value) => ({ value, label: value }))} />
          ) : null}
        </div>
      </div>
    </Dialog>
  );
}

export default function ElasticIpsPage() {
  const { t } = useLingui();
  const def = required("elasticips");
  const project = useCurrentProject();
  const [attaching, setAttaching] = useState<KubeObject | undefined>();
  const patch = useTargetPatch();
  const [canUpdate, canPatch] = useCans(project ? ["update", "patch"].map((verb) => ({ verb, group: def.group, resource: def.plural, namespace: project.namespace })) : []);
  return (
    <ResourceListPage
      def={def}
      rowItems={(object) =>
        canUpdate !== "allowed" || canPatch !== "allowed" || object.metadata.namespace !== project?.namespace
          ? []
          : textAt(object, "spec.target.name") === ""
            ? [{ id: "attach", label: t({ id: "eip.attach.confirm", message: "Attach" }), onSelect: () => setAttaching(object) }]
            : [{ id: "detach", label: t({ id: "eip.detach", message: "Detach" }), onSelect: () => void patch(object, null) }]
      }
    >
      {attaching && project && attaching.metadata.namespace === project.namespace && canUpdate === "allowed" && canPatch === "allowed"
        ? <AttachDialog eip={attaching} namespace={project.namespace} onClose={() => setAttaching(undefined)} />
        : null}
    </ResourceListPage>
  );
}
