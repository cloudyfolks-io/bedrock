import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card, TextField } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import type { ScreenProps } from "../screens";

export function Recovery({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);

  return (
    <Card title={t({ id: "login.recovery.title", message: "Enter a recovery code" })}>
      {error ? <Alert tone="error">{t(errorDescriptor(error))}</Alert> : null}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          api
            .answer({ type: "recovery", code, method: "recovery" }, challenge.csrf ?? "")
            .then((next) => {
              if (next.type === "error") {
                setError(next.error?.code ?? "unknown");
                return;
              }
              onChallenge(next);
            })
            .catch(() => setError("unknown"));
        }}
      >
        <TextField
          label={t({ id: "login.recovery.label", message: "Recovery code" })}
          name="recoveryCode"
          autoComplete="off"
          value={code}
          onChange={setCode}
        />
        <Button variant="primary" type="submit">
          {t({ id: "login.recovery.submit", message: "Verify" })}
        </Button>
      </form>
    </Card>
  );
}
