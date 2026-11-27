import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import { codeFromRejection, useFocusOnError } from "../useAnswer";
import type { ScreenProps } from "../screens";

export function SAxess({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();
  const [checking, setChecking] = useState(false);
  const [notApproved, setNotApproved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const errorRef = useFocusOnError(error !== null);

  async function checkApproval() {
    setChecking(true);
    setNotApproved(false);
    setError(null);
    try {
      const next = await api.answer({ type: "saxess" }, challenge.csrf ?? "");
      if (next.type === "error") {
        setError(next.error?.code ?? "unknown");
      } else {
        if (next.type === "saxess") {
          setError(next.error?.code ?? null);
          setNotApproved(!next.error);
        }
        onChallenge(next);
      }
    } catch (reason) {
      setError(codeFromRejection(reason));
    } finally {
      setChecking(false);
    }
  }

  return (
    <Card title={t({ id: "login.saxess.title", message: "Approve sign-in with sAxess Pro" })}>
      <p>{t({ id: "login.saxess.instructions", message: "Open the sAxess Pro app and approve this sign-in. Then return here and select Check approval." })}</p>
      {error ? (
        <div ref={errorRef}>
          <Alert tone="error">{t(errorDescriptor(error))}</Alert>
        </div>
      ) : null}
      <p role="status">
        {notApproved ? t({ id: "login.saxess.pending", message: "No approval found. You can check again after approving the request." }) : null}
      </p>
      <Button variant="primary" disabled={checking} onClick={checkApproval}>
        {t({ id: "login.saxess.check", message: "Check approval" })}
      </Button>
      <p>{t({ id: "login.saxess.restart", message: "If the request expired or was denied, return to the app where you started sign-in and try again." })}</p>
    </Card>
  );
}
