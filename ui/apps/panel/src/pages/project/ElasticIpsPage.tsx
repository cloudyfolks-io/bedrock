import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { Button, Dialog, Select, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { kubeClient, listKey } from "../../api/kube";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { listAt, textAt } from "../../resources/path";
import { definitionFor, refOf } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { useCurrentProject } from "../../shell/context";
import { useLiveList } from "../../stream/live";

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
  const queryClient = useQueryClient();
  const def = required("elasticips");
  return async (eip, target) => {
    try {
      await kubeClient(http).mergePatch(refOf(def), eip.metadata.namespace, eip.metadata.name, { spec: { target } });
      void queryClient.invalidateQueries({ queryKey: listKey(refOf(def), eip.metadata.namespace, {}) });
      return true;
    } catch (error) {
      toast.show({ tone: "danger", title: t({ id: "action.error.failed", message: "The action failed" }), description: error instanceof Error ? error.message : "" });
      return false;
    }
  };
}

function AttachDialog({ eip, onClose }: { eip: KubeObject; onClose: () => void }) {
  const { t } = useLingui();
  const project = useCurrentProject();
  const namespace = project?.namespace;
  const vms = useLiveList(refOf(required("virtualmachines")), namespace ? { namespace } : {});
  const balancers = useLiveList(refOf(required("loadbalancers")), namespace ? { namespace } : {});
  const [kind, setKind] = useState("VirtualMachine");
  const [name, setName] = useState("");
  const [subnet, setSubnet] = useState("");
  const patch = useTargetPatch();
  const items = (kind === "VirtualMachine" ? vms.data?.items : balancers.data?.items) ?? [];
  const selected = items.find((item) => item.metadata.name === name);
  const subnets = kind === "VirtualMachine" && selected ? (listAt(selected, "spec.networks") as { subnetRef: string }[]).map((entry) => entry.subnetRef) : [];
  const ready = name !== "" && (kind !== "VirtualMachine" || subnet !== "");

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
        <Button variant="primary" disabled={!ready} onClick={() => void patch(eip, { kind, name, ...(kind === "VirtualMachine" ? { subnetRef: subnet } : {}) }).then((done) => done && onClose())}>
          {t({ id: "eip.attach.confirm", message: "Attach" })}
        </Button>
      }
    >
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
      <Select label={t({ id: "eip.attach.target", message: "Target" })} value={name} onValueChange={setName} options={items.map((item) => ({ value: item.metadata.name, label: item.metadata.name }))} />
      {kind === "VirtualMachine" ? (
        <Select label={t({ id: "col.subnet", message: "Subnet" })} value={subnet} onValueChange={setSubnet} options={subnets.map((value) => ({ value, label: value }))} />
      ) : null}
    </Dialog>
  );
}

export default function ElasticIpsPage() {
  const { t } = useLingui();
  const def = required("elasticips");
  const project = useCurrentProject();
  const [attaching, setAttaching] = useState<KubeObject | undefined>();
  const patch = useTargetPatch();
  const [canUpdate] = useCans(project ? [{ verb: "update", group: def.group, resource: def.plural, namespace: project.namespace }] : []);
  return (
    <ResourceListPage
      def={def}
      rowItems={(object) =>
        canUpdate !== "allowed"
          ? []
          : textAt(object, "spec.target.name") === ""
            ? [{ id: "attach", label: t({ id: "eip.attach.confirm", message: "Attach" }), onSelect: () => setAttaching(object) }]
            : [{ id: "detach", label: t({ id: "eip.detach", message: "Detach" }), onSelect: () => void patch(object, null) }]
      }
    >
      {attaching ? <AttachDialog eip={attaching} onClose={() => setAttaching(undefined)} /> : null}
    </ResourceListPage>
  );
}
