import type { WebSocketLike } from "../stream/protocol";

export class FakeSocket implements WebSocketLike {
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: ((event: { code: number }) => void) | null = null;
  onerror: (() => void) | null = null;
  readonly sent: Record<string, unknown>[] = [];
  readonly closeCodes: number[] = [];

  constructor(
    readonly url: string,
    readonly protocols: string[],
  ) {}

  send(data: string): void {
    this.sent.push(JSON.parse(data) as Record<string, unknown>);
  }

  open(): void {
    this.readyState = 1;
    this.onopen?.();
  }

  receive(frame: unknown): void {
    this.onmessage?.({ data: JSON.stringify(frame) });
  }

  receiveRaw(data: string): void {
    this.onmessage?.({ data });
  }

  close(code = 1000): void {
    this.readyState = 3;
    this.closeCodes.push(code);
    this.onclose?.({ code });
  }

  framesOf(op: string): Record<string, unknown>[] {
    return this.sent.filter((frame) => frame.op === op);
  }
}

export interface FakeSockets {
  create: (url: string, protocols: string[]) => FakeSocket;
  sockets: FakeSocket[];
  latest: () => FakeSocket;
}

export function createFakeSockets(): FakeSockets {
  const sockets: FakeSocket[] = [];
  return {
    sockets,
    create: (url, protocols) => {
      const socket = new FakeSocket(url, protocols);
      sockets.push(socket);
      return socket;
    },
    latest: () => {
      const socket = sockets[sockets.length - 1];
      if (!socket) {
        throw new Error("no socket was created");
      }
      return socket;
    },
  };
}
