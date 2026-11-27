import { createContext, useContext, useEffect, useMemo, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { createHttp, isHttpError, type Http } from "./http";
import { objectKey } from "./kube";
import { clearPendingWrites, configureWrites, type PendingWrite } from "./pendingWrites";
import type { KubeList, KubeObject, Session } from "./types";

export const sessionKey = ["session"] as const;

const HttpContext = createContext<Http | null>(null);

export function sessionScope(session: Session): string {
  return JSON.stringify([session.host, session.user.name, session.admin, [...new Set(session.user.groups)].sort(), session.csrf]);
}

export function affectedByWrite(key: readonly unknown[], write: PendingWrite): boolean {
  const [root, group, plural, namespace, type, selector] = key;
  if (root === "kube" && group === write.ref.group && plural === write.ref.plural) {
    const matchingNamespace = namespace === (write.namespace ?? "");
    return type === "object"
      ? matchingNamespace && selector === write.name
      : matchingNamespace || (type === "list" && write.ref.namespaced && namespace === "");
  }
  return root === "admin-inventory" ||
    (root === "projects" && write.ref.plural === "projects") ||
    ((root === "wizard" || root === "ref-options") && group === write.ref.plural);
}

export function HttpProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const http = useMemo(
    () => {
      const client = createHttp({
        fetch: (input, init) => globalThis.fetch(input, init),
        csrf: () => queryClient.getQueryData<Session>(sessionKey)?.csrf,
        onUnauthenticated: () => {
          if (queryClient.isFetching({ queryKey: sessionKey }) === 0) {
            void queryClient.invalidateQueries({ queryKey: sessionKey });
          }
        },
      });
      configureWrites(client,
        (write) => queryClient.invalidateQueries({ predicate: (query) => affectedByWrite(query.queryKey, write) }),
        (write) => {
          const detail = queryClient.getQueryData<KubeObject>(objectKey(write.ref, write.namespace, write.name));
          if (detail?.metadata.uid) return detail.metadata.uid;
          for (const [, list] of queryClient.getQueriesData<KubeList>({
            predicate: (query) => query.queryKey[0] === "kube" && query.queryKey[4] === "list" && affectedByWrite(query.queryKey, write),
          })) {
            const row = list?.items.find((item) => item.metadata.name === write.name && item.metadata.namespace === write.namespace);
            if (row?.metadata.uid) return row.metadata.uid;
          }
          return undefined;
        },
      );
      return client;
    },
    [queryClient],
  );
  useEffect(() => {
    const initial = queryClient.getQueryData<Session>(sessionKey);
    let scope = initial ? sessionScope(initial) : undefined;
    return queryClient.getQueryCache().subscribe((event) => {
      if (event.type !== "updated" || event.query.queryKey[0] !== sessionKey[0]) {
        return;
      }
      const rejected = event.action.type === "error" && isHttpError(event.action.error) && event.action.error.status === 401;
      const session = event.action.type === "success" ? queryClient.getQueryData<Session>(sessionKey) : undefined;
      if (!rejected && !session) {
        return;
      }
      const next = session ? sessionScope(session) : undefined;
      if (scope !== next && (scope !== undefined || rejected)) {
        clearPendingWrites(http);
        queryClient.removeQueries({ predicate: (query) => query.queryKey[0] !== sessionKey[0] });
        queryClient.getMutationCache().clear();
      }
      scope = next;
      if (session) {
        http.resetUnauthenticated();
      }
    });
  }, [queryClient, http]);
  return <HttpContext.Provider value={http}>{children}</HttpContext.Provider>;
}

export function useHttp(): Http {
  const http = useContext(HttpContext);
  if (!http) {
    throw new Error("useHttp needs an HttpProvider");
  }
  return http;
}
