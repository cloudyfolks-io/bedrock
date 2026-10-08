import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Tab, TabList, TabPanel, Tabs } from "@bedrock/design";
import type { KubeObject } from "../api/types";
import { serialUrl, vncUrl } from "./consoleUrl";
import { SerialConsole } from "./SerialConsole";
import { useSessionCheck } from "./useConsoleSession";
import { VncConsole } from "./VncConsole";

export default function ConsoleTab({ vm, allowVnc, allowSerial }: { vm: KubeObject; allowVnc: boolean; allowSerial: boolean }) {
  const { t } = useLingui();
  const [tab, setTab] = useState(allowVnc ? "vnc" : "serial");
  const checkSession = useSessionCheck();
  const vnc = allowVnc ? vncUrl(vm) : undefined;
  const serial = allowSerial ? serialUrl(vm) : undefined;
  if (!vnc && !serial) {
    return <p className="br-help">{t({ id: "console.notRunning", message: "Start the VM to open the console" })}</p>;
  }
  return (
    <Tabs value={tab} onValueChange={setTab}>
      <TabList>
        {vnc ? <Tab value="vnc">{t({ id: "console.vnc", message: "Graphical console" })}</Tab> : null}
        {serial ? <Tab value="serial">{t({ id: "console.serial", message: "Serial console" })}</Tab> : null}
      </TabList>
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
