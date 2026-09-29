import { useEffect, useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import { loginPath } from "../router";
import type { SessionInfo } from "../types";
import { handleAccountError } from "./session";

interface SessionsSectionProps {
  csrf: string;
  onSessionExpired: () => void;
}

export function SessionsSection({ csrf, onSessionExpired }: SessionsSectionProps) {
  const { t } = useLingui();
  const [sessions, setSessions] = useState<SessionInfo[]>([]);
  const [error, setError] = useState<string | null>(null);

  const refresh = () => {
    api.listSessions().then(setSessions).catch((thrown: unknown) => handleAccountError(thrown, onSessionExpired, setError));
  };

  useEffect(refresh, []);

  return (
    <section>
      <h3>{t({ id: "account.sessions.title", message: "Sessions" })}</h3>
      {error ? <Alert tone="error">{t(errorDescriptor(error))}</Alert> : null}
      <ul>
        {sessions.map((session) => (
          <li key={session.id}>
            {session.current ? t({ id: "account.sessions.current", message: "This device" }) : session.userAgent}
            <Button
              variant="secondary"
              onClick={() => {
                setError(null);
                api
                  .revokeSession(session.id, csrf)
                  .then(refresh)
                  .catch((thrown: unknown) => handleAccountError(thrown, onSessionExpired, setError));
              }}
            >
              {t({ id: "account.sessions.revoke", message: "Revoke" })}
            </Button>
          </li>
        ))}
      </ul>
      <Button
        variant="primary"
        onClick={() => {
          setError(null);
          api
            .logout(csrf)
            .then(() => window.location.assign(loginPath()))
            .catch((thrown: unknown) => handleAccountError(thrown, onSessionExpired, setError));
        }}
      >
        {t({ id: "account.sessions.logout", message: "Sign out" })}
      </Button>
    </section>
  );
}
