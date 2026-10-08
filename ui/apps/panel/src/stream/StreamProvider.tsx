import { createContext, useContext, useEffect, useMemo, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { sessionKey } from "../api/HttpProvider";
import { Multiplexer } from "./multiplexer";
import type { WebSocketLike } from "./protocol";

const MultiplexerContext = createContext<Multiplexer | null>(null);

export function streamUrl(location: Pick<Location, "protocol" | "host">): string {
  return `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}/proxy/v1/stream`;
}

function browserSocket(url: string, protocols: string[]): WebSocketLike {
  return new WebSocket(url, protocols) as unknown as WebSocketLike;
}

interface StreamProviderProps {
  createSocket?: (url: string, protocols: string[]) => WebSocketLike;
  children: ReactNode;
}

export function StreamProvider({ createSocket, children }: StreamProviderProps) {
  const queryClient = useQueryClient();
  const multiplexer = useMemo(
    () =>
      new Multiplexer({
        url: streamUrl(window.location),
        createSocket: createSocket ?? browserSocket,
        onSessionEnded: () => queryClient.invalidateQueries({ queryKey: sessionKey }),
      }),
    [createSocket, queryClient],
  );
  useEffect(() => () => multiplexer.close(), [multiplexer]);
  return <MultiplexerContext.Provider value={multiplexer}>{children}</MultiplexerContext.Provider>;
}

export function useMultiplexer(): Multiplexer {
  const multiplexer = useContext(MultiplexerContext);
  if (!multiplexer) {
    throw new Error("useMultiplexer needs a StreamProvider");
  }
  return multiplexer;
}
