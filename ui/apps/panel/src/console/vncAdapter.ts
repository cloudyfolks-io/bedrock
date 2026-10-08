import RFB from "@novnc/novnc";

export interface VncHandlers {
  onConnect: () => void;
  onDisconnect: (clean: boolean) => void;
  onSecurityFailure: (reason: string) => void;
}

export interface VncHandle {
  disconnect: () => void;
  sendCtrlAltDel: () => void;
  setViewOnly: (viewOnly: boolean) => void;
  setScale: (on: boolean) => void;
}

export function createVnc(target: HTMLElement, url: string, handlers: VncHandlers): VncHandle {
  const rfb = new RFB(target, url, { wsProtocols: ["binary"] });
  rfb.scaleViewport = true;
  rfb.addEventListener("connect", () => handlers.onConnect());
  rfb.addEventListener("disconnect", (event) => handlers.onDisconnect((event as CustomEvent<{ clean: boolean }>).detail.clean));
  rfb.addEventListener("securityfailure", (event) => handlers.onSecurityFailure(String((event as CustomEvent<{ reason?: string }>).detail.reason ?? "")));
  return {
    disconnect: () => rfb.disconnect(),
    sendCtrlAltDel: () => rfb.sendCtrlAltDel(),
    setViewOnly: (viewOnly) => {
      rfb.viewOnly = viewOnly;
    },
    setScale: (on) => {
      rfb.scaleViewport = on;
    },
  };
}
