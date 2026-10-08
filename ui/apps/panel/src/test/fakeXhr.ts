import { vi } from "vitest";

export class FakeXhr {
  static instances: FakeXhr[] = [];
  method = "";
  url = "";
  headers: Record<string, string> = {};
  body: unknown;
  withCredentials = false;
  status = 0;
  aborted = false;
  upload: { onprogress: ((event: { loaded: number; total: number }) => void) | null } = { onprogress: null };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;

  constructor() {
    FakeXhr.instances.push(this);
  }

  open(method: string, url: string): void {
    this.method = method;
    this.url = url;
  }

  setRequestHeader(name: string, value: string): void {
    this.headers[name] = value;
  }

  send(body: unknown): void {
    this.body = body;
  }

  abort(): void {
    this.aborted = true;
    this.onabort?.();
  }

  progress(loaded: number, total: number): void {
    this.upload.onprogress?.({ loaded, total });
  }

  finish(status: number): void {
    this.status = status;
    this.onload?.();
  }

  fail(): void {
    this.onerror?.();
  }

  static install(): void {
    FakeXhr.instances = [];
    vi.stubGlobal("XMLHttpRequest", FakeXhr);
  }

  static latest(): FakeXhr {
    const instance = FakeXhr.instances[FakeXhr.instances.length - 1];
    if (!instance) {
      throw new Error("no XMLHttpRequest was created");
    }
    return instance;
  }
}
