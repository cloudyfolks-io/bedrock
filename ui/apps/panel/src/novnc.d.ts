declare module "@novnc/novnc" {
  interface RfbOptions {
    wsProtocols?: string[];
  }

  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, url: string, options?: RfbOptions);
    viewOnly: boolean;
    scaleViewport: boolean;
    disconnect(): void;
    sendCtrlAltDel(): void;
  }
}
