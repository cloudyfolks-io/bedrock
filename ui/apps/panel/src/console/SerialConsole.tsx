import { useEffect, useRef, useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button } from "@bedrock/design";
import type { SerialHandle } from "./serialAdapter";

type State = "connecting" | "connected" | "disconnected";

interface Props {
  url: string;
  onDisconnect: (code: number) => void;
}

export function SerialConsole({ url, onDisconnect }: Props) {
  const { t } = useLingui();
  const container = useRef<HTMLDivElement>(null);
  const [state, setState] = useState<State>("connecting");
  const [attempt, setAttempt] = useState(0);
  const notify = useRef(onDisconnect);
  notify.current = onDisconnect;

  useEffect(() => {
    let cancelled = false;
    let created: SerialHandle | undefined;
    setState("connecting");
    void import("./serialAdapter").then((adapter) => {
      if (cancelled || !container.current) {
        return;
      }
      created = adapter.createSerial(container.current, url, {
        onOpen: () => setState("connected"),
        onClose: (code) => {
          setState("disconnected");
          notify.current(code);
        },
      });
    });
    return () => {
      cancelled = true;
      created?.disconnect();
    };
  }, [url, attempt]);

  const message = {
    connecting: t({ id: "console.state.connecting", message: "Connecting to the console" }),
    connected: t({ id: "console.state.connected", message: "Connected" }),
    disconnected: t({ id: "console.state.disconnected", message: "The console disconnected" }),
  }[state];

  return (
    <section aria-label={t({ id: "console.serial", message: "Serial console" })}>
      <p role="status" className="br-help">
        {message}
      </p>
      {state === "disconnected" ? (
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
