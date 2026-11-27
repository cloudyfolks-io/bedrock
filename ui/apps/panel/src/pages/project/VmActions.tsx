import { useMemo } from "react";
import { useLingui } from "@lingui/react/macro";
import { Button } from "@bedrock/design";
import { Menu } from "@base-ui/react/menu";
import { useCans, type Access, type Check } from "../../authz/authz";
import type { KubeObject } from "../../api/types";
import { textAt } from "../../resources/path";
import { definitionFor } from "../../resources/registry";
import { useActionRunner } from "../../resources/useActionRunner";
import styles from "./project.module.css";

export { actionStates } from "../../resources/vmActionStates";

function vmDefinition() {
  const def = definitionFor("virtualmachines");
  if (!def) {
    throw new Error("missing virtualmachines definition");
  }
  return def;
}

export function useVmAccess(namespace: string | undefined): { actions: Access[]; vnc: Access; serial: Access } {
  const def = vmDefinition();
  const checks = useMemo<Check[]>(() => {
    if (namespace === undefined) {
      return [];
    }
    const base = { group: def.group, resource: def.plural, namespace };
    return [
      ...def.actions.map((action) => ({ ...base, verb: action.verb, ...(action.subresource === undefined ? {} : { subresource: action.subresource }) })),
      { ...base, verb: "get", subresource: "console/vnc" },
      { ...base, verb: "get", subresource: "console/serial" },
    ];
  }, [def, namespace]);
  const access = useCans(checks);
  return { actions: access.slice(0, def.actions.length), vnc: access[def.actions.length] ?? "pending", serial: access[def.actions.length + 1] ?? "pending" };
}

export function VmActions({ vm, access, pending }: { vm: KubeObject; access: Access[]; pending: boolean }) {
  const def = vmDefinition();
  const { t, i18n } = useLingui();
  const runner = useActionRunner(def);
  const phase = textAt(vm, "status.phase");
  const primaryId = phase === "Running" ? "stop" : "start";
  const allowed = def.actions.filter((_, index) => access[index] === "allowed");
  const primary = allowed.find((action) => action.id === primaryId && action.enabledWhen(vm));
  const overflow = allowed.filter((action) => action !== primary);
  const destructive = overflow.find((action) => action.kind === "delete");
  const other = overflow.filter((action) => action !== destructive);
  if (allowed.length === 0) return null;

  return (
    <div className={styles.vmActions}>
      {primary ? (
        <Button variant="primary" disabled={pending} onClick={() => runner.request(primary, vm)}>
          {i18n._(primary.label)}
        </Button>
      ) : null}
      {overflow.length > 0 ? (
        <Menu.Root>
          <Menu.Trigger
            className="br-button br-button--secondary"
            aria-label={t({ id: "vmDetail.actions.more", message: "More VM actions" })}
            disabled={pending}
          >
            <span aria-hidden="true">⋯</span>
          </Menu.Trigger>
          <Menu.Portal>
            <Menu.Positioner className={styles.menuPositioner} align="end" sideOffset={6}>
              <Menu.Popup className={`br-menu-popup ${styles.actionMenu}`}>
                {other.map((action) => (
                  <Menu.Item
                    key={action.id}
                    className="br-menu-item"
                    disabled={pending || !action.enabledWhen(vm)}
                    onClick={() => runner.request(action, vm)}
                  >
                    {i18n._(action.label)}
                  </Menu.Item>
                ))}
                {other.length > 0 && destructive ? <Menu.Separator className={styles.actionSeparator} /> : null}
                {destructive ? (
                  <Menu.Item
                    className="br-menu-item"
                    data-tone="danger"
                    disabled={pending || !destructive.enabledWhen(vm)}
                    onClick={() => runner.request(destructive, vm)}
                  >
                    {i18n._(destructive.label)}
                  </Menu.Item>
                ) : null}
              </Menu.Popup>
            </Menu.Positioner>
          </Menu.Portal>
        </Menu.Root>
      ) : null}
      {pending ? null : runner.dialogs}
    </div>
  );
}
