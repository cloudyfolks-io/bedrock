import { createContext, useContext, useMemo, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { createHttp, type Http } from "./http";
import type { Session } from "./types";

export const sessionKey = ["session"] as const;

const HttpContext = createContext<Http | null>(null);

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
  return <HttpContext.Provider value={http}>{children}</HttpContext.Provider>;
}

export function useHttp(): Http {
  const http = useContext(HttpContext);
  if (!http) {
    throw new Error("useHttp needs an HttpProvider");
  }
  return http;
}
