import {
  parseServerFrame,
  streamSubprotocol,
  subscriptionKey,
  type ClientFrame,
  type ServerFrame,
  type StreamHandlers,
  type StreamKey,
  type StreamStatus,
  type Subscription,
  type WebSocketLike,
} from "./protocol";

export interface MultiplexerOptions {
  url: string;
  createSocket: (url: string, protocols: string[]) => WebSocketLike;
  random?: () => number;
  onSessionEnded: () => void;
}

interface Entry {
  id: string;
  key: StreamKey;
  subscription: Subscription;
  handlers: Set<StreamHandlers>;
  lastVersion: string | undefined;
  confirmed: boolean;
  retries: number;
  retryTimer: ReturnType<typeof setTimeout> | undefined;
}

const socketOpen = 1;
const pingEveryMs = 25_000;
const silenceLimitMs = 60_000;
const idleCloseMs = 5_000;
const sessionEndedCode = 4401;
const defaultLimit = 64;

export function backoffDelay(attempt: number, random: () => number): number {
  return Math.min(30_000, 1000 * 2 ** attempt) * (0.5 + 0.5 * random());
}

export class Multiplexer {
  private readonly options: MultiplexerOptions;
  private readonly random: () => number;
  private readonly entries = new Map<StreamKey, Entry>();
  private readonly listeners = new Set<(status: StreamStatus) => void>();
  private socket: WebSocketLike | null = null;
  private current: StreamStatus = "idle";
  private counter = 0;
  private attempt = 0;
  private limit = defaultLimit;
  private reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  private idleTimer: ReturnType<typeof setTimeout> | undefined;
  private pingTimer: ReturnType<typeof setInterval> | undefined;
  private watchdog: ReturnType<typeof setTimeout> | undefined;

  constructor(options: MultiplexerOptions) {
    this.options = options;
    this.random = options.random ?? Math.random;
  }

  status(): StreamStatus {
    return this.current;
  }

  onStatus(listener: (status: StreamStatus) => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  acquire(subscription: Subscription, handlers: StreamHandlers): () => void {
    clearTimeout(this.idleTimer);
    const key = subscriptionKey(subscription);
    let entry = this.entries.get(key);
    if (!entry) {
      if (this.entries.size >= this.limit) {
        handlers.onError("limit_exceeded", "too many subscriptions on this connection");
        return () => {};
      }
      this.counter += 1;
      entry = { id: `s${this.counter}`, key, subscription, handlers: new Set(), lastVersion: undefined, confirmed: false, retries: 0, retryTimer: undefined };
      this.entries.set(key, entry);
    }
    entry.handlers.add(handlers);
    if (entry.confirmed) {
      handlers.onSubscribed?.();
    }
    this.ensureConnected();
    if (this.current === "live" && !entry.confirmed) {
      this.sendSubscribe(entry);
    }
    const owner = entry;
    return () => this.release(owner, handlers);
  }

  close(): void {
    this.teardownSocket();
    clearTimeout(this.reconnectTimer);
    clearTimeout(this.idleTimer);
    for (const entry of this.entries.values()) {
      clearTimeout(entry.retryTimer);
    }
    this.entries.clear();
    this.setStatus("idle");
  }

  private setStatus(next: StreamStatus): void {
    if (next === this.current) {
      return;
    }
    this.current = next;
    for (const listener of [...this.listeners]) {
      listener(next);
    }
  }

  private release(entry: Entry, handlers: StreamHandlers): void {
    if (this.entries.get(entry.key) !== entry) {
      return;
    }
    entry.handlers.delete(handlers);
    if (entry.handlers.size > 0) {
      return;
    }
    this.entries.delete(entry.key);
    clearTimeout(entry.retryTimer);
    if (this.current === "live") {
      this.send({ op: "unsubscribe", id: entry.id });
    }
    this.scheduleIdleClose();
  }

  private scheduleIdleClose(): void {
    if (this.entries.size > 0) {
      return;
    }
    clearTimeout(this.idleTimer);
    this.idleTimer = setTimeout(() => {
      if (this.entries.size === 0) {
        this.teardownSocket();
        clearTimeout(this.reconnectTimer);
        this.setStatus("idle");
      }
    }, idleCloseMs);
  }

  private ensureConnected(): void {
    if (this.socket || this.reconnectTimer !== undefined || this.current === "ended") {
      return;
    }
    this.connect();
  }

  private connect(): void {
    this.reconnectTimer = undefined;
    if (this.current === "idle") {
      this.setStatus("connecting");
    }
    const socket = this.options.createSocket(this.options.url, [streamSubprotocol]);
    this.socket = socket;
    socket.onopen = () => this.resetWatchdog();
    socket.onmessage = (event) => this.handleMessage(event.data);
    socket.onclose = (event) => this.handleClose(event.code);
    socket.onerror = () => {};
  }

  private teardownSocket(): void {
    clearInterval(this.pingTimer);
    clearTimeout(this.watchdog);
    const socket = this.socket;
    this.socket = null;
    if (socket) {
      socket.onopen = null;
      socket.onmessage = null;
      socket.onclose = null;
      socket.onerror = null;
      socket.close();
    }
  }

  private resetWatchdog(): void {
    clearTimeout(this.watchdog);
    this.watchdog = setTimeout(() => {
      this.teardownSocket();
      this.handleClose(1006);
    }, silenceLimitMs);
  }

  private send(frame: ClientFrame): void {
    if (this.socket && this.socket.readyState === socketOpen) {
      this.socket.send(JSON.stringify(frame));
    }
  }

  private sendSubscribe(entry: Entry): void {
    const first = entry.handlers.values().next().value;
    if (!first || this.current !== "live") {
      return;
    }
    const sub = entry.subscription;
    this.send({
      op: "subscribe",
      id: entry.id,
      group: sub.group,
      version: sub.version,
      resource: sub.resource,
      namespace: sub.namespace ?? "",
      labelSelector: sub.labelSelector ?? "",
      fieldSelector: sub.fieldSelector ?? "",
      resourceVersion: entry.lastVersion ?? first.resourceVersion(),
    });
  }

  private handleClose(code: number): void {
    clearInterval(this.pingTimer);
    clearTimeout(this.watchdog);
    this.socket = null;
    for (const entry of this.entries.values()) {
      entry.confirmed = false;
      clearTimeout(entry.retryTimer);
    }
    if (code === sessionEndedCode) {
      this.setStatus("ended");
      this.options.onSessionEnded();
      return;
    }
    if (this.entries.size === 0) {
      this.setStatus("idle");
      return;
    }
    this.setStatus("reconnecting");
    const delay = backoffDelay(this.attempt, this.random);
    this.attempt += 1;
    this.reconnectTimer = setTimeout(() => this.connect(), delay);
  }

  private handleMessage(text: string): void {
    const frame = parseServerFrame(text);
    if (!frame) {
      return;
    }
    this.resetWatchdog();
    this.dispatch(frame);
  }

  private entryById(id: string): Entry | undefined {
    return [...this.entries.values()].find((entry) => entry.id === id);
  }

  private dispatch(frame: ServerFrame): void {
    switch (frame.type) {
      case "hello":
        this.onHello(frame.limits.maxSubscriptions);
        return;
      case "subscribed": {
        const entry = this.entryById(frame.id);
        if (entry) {
          entry.confirmed = true;
          entry.retries = 0;
          [...entry.handlers].forEach((handler) => handler.onSubscribed?.());
        }
        return;
      }
      case "event":
      case "bookmark": {
        const entry = this.entryById(frame.id);
        if (entry) {
          entry.lastVersion = frame.resourceVersion;
          [...entry.handlers].forEach((handler) => handler.onEvent(frame));
        }
        return;
      }
      case "relist": {
        const entry = this.entryById(frame.id);
        if (entry) {
          this.entries.delete(entry.key);
          clearTimeout(entry.retryTimer);
          [...entry.handlers].forEach((handler) => handler.onRelist());
          this.scheduleIdleClose();
        }
        return;
      }
      case "error":
        this.onErrorFrame(frame);
        return;
      case "pong":
        return;
    }
  }

  private onHello(maxSubscriptions: number): void {
    this.attempt = 0;
    this.limit = maxSubscriptions;
    this.setStatus("live");
    clearInterval(this.pingTimer);
    this.pingTimer = setInterval(() => this.send({ op: "ping" }), pingEveryMs);
    for (const entry of this.entries.values()) {
      this.sendSubscribe(entry);
    }
  }

  private onErrorFrame(frame: Extract<ServerFrame, { type: "error" }>): void {
    const entry = frame.id ? this.entryById(frame.id) : undefined;
    if (!entry) {
      return;
    }
    if (frame.code === "unavailable") {
      const delay = backoffDelay(entry.retries, this.random);
      entry.retries += 1;
      entry.confirmed = false;
      entry.retryTimer = setTimeout(() => this.sendSubscribe(entry), delay);
      return;
    }
    this.entries.delete(entry.key);
    [...entry.handlers].forEach((handler) => handler.onError(frame.code, frame.message));
    this.scheduleIdleClose();
  }
}
