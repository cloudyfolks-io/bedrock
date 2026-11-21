import { createContext, useContext, useEffect, useMemo, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { createHttp, isHttpError, type Http } from "./http";
import type { Session } from "./types";

export const sessionKey = ["session"] as const;

const HttpContext = createContext<Http | null>(null);

export function sessionScope(session: Session): string {
  return JSON.stringify([session.host, session.user.name, session.admin, [...new Set(session.user.groups)].sort(), session.csrf]);
}

export function HttpProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const http = useMemo(
    () =>
      createHttp({
        fetch: (input, init) => globalThis.fetch(input, init),
        csrf: () => queryClient.getQueryData<Session>(sessionKey)?.csrf,
        onUnauthenticated: () => {
          if (queryClient.isFetching({ queryKey: sessionKey }) === 0) {
            void queryClient.invalidateQueries({ queryKey: sessionKey });
          }
        },
      }),
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
