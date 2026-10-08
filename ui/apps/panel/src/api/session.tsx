import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { useLocation } from "react-router";
import { Alert, Button, Card } from "@bedrock/design";
import { Spinner } from "../components/Spinner";
import { useLocationActions } from "../location";
import { readSession, removeSession, writeSession } from "../storage";
import { sessionKey, useHttp } from "./HttpProvider";
import { isHttpError, type Http } from "./http";
import type { Session } from "./types";

export { sessionKey };

const sessionStaleMs = 5 * 60 * 1000;
const loopWindowMs = 10 * 1000;

export function safeReturnTo(path: string): string {
  const local = path.startsWith("/") && !path.startsWith("//") && !path.startsWith("/\\");
  return local ? path : "/";
}

export function loginUrl(returnTo: string): string {
  return `/oauth/login?returnTo=${encodeURIComponent(safeReturnTo(returnTo))}`;
}

function fetchSession(http: Http): Promise<Session> {
  return http.request<Session>("GET", "/proxy/v1/session");
}

export function useSession() {
  const http = useHttp();
  return useQuery({
    queryKey: sessionKey,
    queryFn: () => fetchSession(http),
    staleTime: sessionStaleMs,
    retry: false,
    refetchOnWindowFocus: true,
  });
}

function signedInRecently(now: number): boolean {
  const stored = Number(readSession("signin"));
  return Number.isFinite(stored) && stored > 0 && now - stored < loopWindowMs;
}

function GateCard({ children }: { children: ReactNode }) {
  const { t } = useLingui();
  return (
    <Card title={t({ id: "session.gate.error.title", message: "Sign in did not work" })}>
      <Alert tone="error">{t({ id: "session.gate.error.body", message: "The panel could not read your session." })}</Alert>
      {children}
    </Card>
  );
}

export function SessionGate({ children }: { children: ReactNode }) {
  const { t } = useLingui();
  const query = useSession();
  const location = useLocation();
  const { assign } = useLocationActions();
  const [blocked, setBlocked] = useState(false);
  const redirected = useRef(false);
  const returnTo = location.pathname + location.search;
  const unauthenticated = isHttpError(query.error) && query.error.status === 401;

  useEffect(() => {
    if (query.isSuccess) {
      removeSession("signin");
    }
  }, [query.isSuccess]);

  useEffect(() => {
    if (!unauthenticated || redirected.current) {
      return;
    }
    redirected.current = true;
    if (signedInRecently(Date.now())) {
      setBlocked(true);
      return;
    }
    writeSession("signin", String(Date.now()));
    assign(loginUrl(returnTo));
  }, [unauthenticated, assign, returnTo]);

  if (query.data && !unauthenticated) {
    return <>{children}</>;
  }
  if (unauthenticated && blocked) {
    return (
      <GateCard>
        <a className="br-button br-button--primary" href={loginUrl(returnTo)}>
          {t({ id: "common.action.retry", message: "Retry" })}
        </a>
      </GateCard>
    );
  }
  if (query.isError && !unauthenticated) {
    return (
      <GateCard>
        <Button variant="primary" onClick={() => void query.refetch()}>
          {t({ id: "common.action.retry", message: "Retry" })}
        </Button>
      </GateCard>
    );
  }
  return <Spinner />;
}
