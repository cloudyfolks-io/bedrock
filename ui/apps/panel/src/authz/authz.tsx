import { createContext, useContext, useEffect, useMemo, useSyncExternalStore, type ReactNode } from "react";
import { useHttp } from "../api/HttpProvider";
import { useSession } from "../api/session";
import type { Http } from "../api/http";
import type { Check } from "../api/types";

export type { Check };
export type Access = "pending" | "allowed" | "denied";

const staleMs = 30_000;
const chunkSize = 100;

export function checkKey(check: Check): string {
  return JSON.stringify([check.verb, check.group ?? "", check.resource, check.subresource ?? "", check.namespace ?? "", check.name ?? ""]);
}

interface Result {
  access: "allowed" | "denied";
  fetchedAt: number;
}

interface AuthzStore {
  request(check: Check): void;
  accessOf(check: Check): Access;
  subscribe(listener: () => void): () => void;
  version(): number;
}

function chunk<T>(items: T[], size: number): T[][] {
  return Array.from({ length: Math.ceil(items.length / size) }, (_, index) => items.slice(index * size, (index + 1) * size));
}

function createAuthzStore(http: Http): AuthzStore {
  let results = new Map<string, Result>();
  let inflight = new Set<string>();
  let queued = new Map<string, Check>();
  let scheduled = false;
  let counter = 0;
  const listeners = new Set<() => void>();

  function notify(): void {
    counter += 1;
    listeners.forEach((listener) => listener());
  }

  async function send(checks: Check[], generation: Map<string, Result>): Promise<void> {
    const outcome = await http
      .request<{ results: { allowed: boolean }[] }>("POST", "/proxy/v1/authz", { body: { checks } })
      .then((answer) => answer.results.map((result) => (result.allowed ? ("allowed" as const) : ("denied" as const))))
      .catch(() => checks.map(() => "denied" as const));
    if (generation !== results) {
      return;
    }
    checks.forEach((check, index) => {
      const key = checkKey(check);
      inflight.delete(key);
      results.set(key, { access: outcome[index] ?? "denied", fetchedAt: Date.now() });
    });
    notify();
  }

  function flush(): void {
    scheduled = false;
    const batch = [...queued.values()];
    queued = new Map();
    const generation = results;
    chunk(batch, chunkSize).forEach((part) => void send(part, generation));
  }

  return {
    request(check) {
      const key = checkKey(check);
      const known = results.get(key);
      if (inflight.has(key) || queued.has(key) || (known && Date.now() - known.fetchedAt < staleMs)) {
        return;
      }
      inflight.add(key);
      queued.set(key, check);
      if (!scheduled) {
        scheduled = true;
        queueMicrotask(() => setTimeout(flush, 0));
      }
    },
    accessOf: (check) => results.get(checkKey(check))?.access ?? "pending",
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    version: () => counter,
  };
}

const AuthzContext = createContext<AuthzStore | null>(null);

export function AuthzProvider({ children }: { children: ReactNode }) {
  const http = useHttp();
  const user = useSession().data?.user.name;
  const store = useMemo(() => createAuthzStore(http), [http, user]);
  return <AuthzContext.Provider value={store}>{children}</AuthzContext.Provider>;
}

function useStore(): AuthzStore {
  const store = useContext(AuthzContext);
  if (!store) {
    throw new Error("authz hooks need an AuthzProvider");
  }
  return store;
}

export function useCans(checks: Check[]): Access[] {
  const store = useStore();
  useSyncExternalStore(store.subscribe, store.version);
  const signature = checks.map(checkKey).join("|");
  useEffect(() => {
    checks.forEach((check) => store.request(check));
  }, [store, signature]);
  return checks.map((check) => store.accessOf(check));
}

export function useCan(check: Check): Access {
  return useCans([check])[0] ?? "pending";
}

export function Can({ check, children }: { check: Check; children: ReactNode }) {
  return useCan(check) === "allowed" ? <>{children}</> : null;
}
