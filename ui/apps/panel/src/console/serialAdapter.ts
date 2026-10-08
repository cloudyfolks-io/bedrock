import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";

export interface SerialHandlers {
  onOpen: () => void;
  onClose: (code: number) => void;
}

export interface SerialHandle {
  disconnect: () => void;
  focus: () => void;
}

export function createSerial(target: HTMLElement, url: string, handlers: SerialHandlers): SerialHandle {
  const terminal = new Terminal({ cursorBlink: true });
  const fit = new FitAddon();
  terminal.loadAddon(fit);
  terminal.open(target);
  fit.fit();
  const socket = new WebSocket(url);
  socket.binaryType = "arraybuffer";
  const encoder = new TextEncoder();
  socket.onopen = () => handlers.onOpen();
  socket.onmessage = (event) => terminal.write(new Uint8Array(event.data as ArrayBuffer));
  socket.onclose = (event) => handlers.onClose(event.code);
  terminal.onData((data) => {
    if (socket.readyState === WebSocket.OPEN) {
      socket.send(encoder.encode(data));
    }
  });
  return {
    disconnect: () => {
      socket.onclose = null;
      socket.close();
      terminal.dispose();
    },
    focus: () => terminal.focus(),
  };
}
