import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Tabs as BaseTabs } from "@base-ui/react/tabs";
import { Tab, TabPanel, Tabs } from "@bedrock/design";
import type { KubeObject } from "../api/types";
import { EmptyState } from "../components/EmptyState";
import { textAt } from "../resources/path";
import { serialUrl, vncUrl } from "./consoleUrl";
import { SerialConsole } from "./SerialConsole";
import { useSessionCheck } from "./useConsoleSession";
import { VncConsole } from "./VncConsole";

export default function ConsoleTab({ vm, allowVnc, allowSerial }: { vm: KubeObject; allowVnc: boolean; allowSerial: boolean }) {
  const { t } = useLingui();
  const [selection, setSelection] = useState<string>();
  const checkSession = useSessionCheck();
  const vnc = allowVnc ? vncUrl(vm) : undefined;
  const serial = allowSerial ? serialUrl(vm) : undefined;
  if (!vnc && !serial) {
    const phase = textAt(vm, "status.phase");
    return <EmptyState title={phase === "Stopped" || phase === "Failed"
      ? t({ id: "console.notRunning", message: "Start the VM to open the console" })
      : t({ id: "console.noEndpoint", message: "No console endpoint is available" })} />;
  }
  const tab = (selection === "vnc" && vnc) || (selection === "serial" && serial) ? selection : vnc ? "vnc" : "serial";
  return (
    <Tabs value={tab} onValueChange={setSelection}>
      <BaseTabs.List className="br-tab-list" aria-label={t({ id: "vmDetail.tab.console", message: "Console" })}>
        {vnc ? <Tab value="vnc">{t({ id: "console.vnc", message: "Graphical console" })}</Tab> : null}
        {serial ? <Tab value="serial">{t({ id: "console.serial", message: "Serial console" })}</Tab> : null}
      </BaseTabs.List>
      {vnc ? (
        <TabPanel value="vnc">
          <VncConsole url={vnc} onDisconnect={(clean) => (clean ? undefined : checkSession())} />
        </TabPanel>
      ) : null}
      {serial ? (
        <TabPanel value="serial">
          <SerialConsole url={serial} onDisconnect={(code) => (code === 4401 ? checkSession() : undefined)} />
        </TabPanel>
      ) : null}
    </Tabs>
  );
}
