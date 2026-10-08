import { useMemo } from "react";
import { useLingui } from "@lingui/react/macro";
import { Button } from "@bedrock/design";
import { useCans, type Access, type Check } from "../../authz/authz";
import type { KubeObject } from "../../api/types";
import { definitionFor } from "../../resources/registry";
import { useActionRunner } from "../../resources/useActionRunner";
import { actionStates } from "../../resources/vmActionStates";

export { actionStates };

function vmDefinition() {
  const def = definitionFor("virtualmachines");
  if (!def) {
    throw new Error("missing virtualmachines definition");
  }
  return def;
}

export function useVmAccess(namespace: string | undefined): { actions: Access[]; console: Access } {
  const def = vmDefinition();
  const checks = useMemo<Check[]>(() => {
    if (namespace === undefined) {
      return [];
    }
    const base = { group: def.group, resource: def.plural, namespace };
    return [
      ...def.actions.map((action) => ({ ...base, verb: action.verb, ...(action.subresource === undefined ? {} : { subresource: action.subresource }) })),
      { ...base, verb: "get", subresource: "console/vnc" },
    ];
  }, [def, namespace]);
  const access = useCans(checks);
  return { actions: access.slice(0, def.actions.length), console: access[def.actions.length] ?? "pending" };
}

export function VmActions({ vm, access }: { vm: KubeObject; access: Access[] }) {
  const def = vmDefinition();
  const { i18n } = useLingui();
  const runner = useActionRunner(def);
  const states = actionStates(vm);
  return (
    <>
      {def.actions.map((action, index) =>
        access[index] === "allowed" ? (
          <Button
            key={action.id}
            variant={action.kind === "delete" ? "danger" : "secondary"}
            disabled={states[action.id as keyof typeof states] !== "enabled"}
            onClick={() => runner.request(action, vm)}
          >
            {i18n._(action.label)}
          </Button>
        ) : null,
      )}
      {runner.dialogs}
    </>
  );
}
