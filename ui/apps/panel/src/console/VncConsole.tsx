import { useEffect, useRef, useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Switch } from "@bedrock/design";
import type { VncHandle } from "./vncAdapter";

type State = "connecting" | "connected" | "disconnected" | "securityfailure";

interface Props {
  url: string;
  onDisconnect: (clean: boolean) => void;
}

export function VncConsole({ url, onDisconnect }: Props) {
  const { t } = useLingui();
  const container = useRef<HTMLDivElement>(null);
  const handle = useRef<VncHandle | undefined>(undefined);
  const [state, setState] = useState<State>("connecting");
  const [attempt, setAttempt] = useState(0);
  const [viewOnly, setViewOnly] = useState(false);
  const [scale, setScale] = useState(true);
  const notify = useRef(onDisconnect);
  notify.current = onDisconnect;

  useEffect(() => {
    let cancelled = false;
    let created: VncHandle | undefined;
    setState("connecting");
    void import("./vncAdapter").then((adapter) => {
      if (cancelled || !container.current) {
        return;
      }
      created = adapter.createVnc(container.current, url, {
        onConnect: () => setState("connected"),
        onDisconnect: (clean) => {
          setState("disconnected");
          notify.current(clean);
        },
        onSecurityFailure: () => setState("securityfailure"),
      });
      handle.current = created;
    });
    return () => {
      cancelled = true;
      created?.disconnect();
      handle.current = undefined;
    };
  }, [url, attempt]);

  const message = {
    connecting: t({ id: "console.state.connecting", message: "Connecting to the console" }),
    connected: t({ id: "console.state.connected", message: "Connected" }),
    disconnected: t({ id: "console.state.disconnected", message: "The console disconnected" }),
    securityfailure: t({ id: "console.state.securityfailure", message: "The console refused the connection" }),
  }[state];

  return (
    <section aria-label={t({ id: "console.vnc", message: "Graphical console" })}>
      <div className="br-toolbar">
        <Button variant="secondary" disabled={state !== "connected"} onClick={() => handle.current?.sendCtrlAltDel()}>
          {t({ id: "console.ctrlAltDel", message: "Send Ctrl+Alt+Del" })}
        </Button>
        <Switch
          label={t({ id: "console.viewOnly", message: "View only" })}
          checked={viewOnly}
          onCheckedChange={(next) => {
            setViewOnly(next);
            handle.current?.setViewOnly(next);
          }}
        />
        <Switch
          label={t({ id: "console.scale", message: "Scale to fit" })}
          checked={scale}
          onCheckedChange={(next) => {
            setScale(next);
            handle.current?.setScale(next);
          }}
        />
        <Button variant="secondary" onClick={() => void container.current?.requestFullscreen?.()}>
          {t({ id: "console.fullscreen", message: "Full screen" })}
        </Button>
      </div>
      <p role="status" className="br-help">
        {message}
      </p>
      {state === "disconnected" || state === "securityfailure" ? (
        <Alert tone="error">
          {message}{" "}
          <Button variant="primary" onClick={() => setAttempt((value) => value + 1)}>
            {t({ id: "console.reconnect", message: "Reconnect" })}
          </Button>
        </Alert>
      ) : null}
      <div ref={container} dir="ltr" className="br-console" />
    </section>
  );
}
