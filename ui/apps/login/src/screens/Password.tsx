import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card, TextField } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import type { ScreenProps } from "../screens";

export function Password({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);

  return (
    <Card title={t({ id: "login.password.title", message: "Enter your password" })}>
      {error ? <Alert tone="error">{t(errorDescriptor(error))}</Alert> : null}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          api
            .answer({ type: "password", password }, challenge.csrf ?? "")
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
          label={t({ id: "login.password.label", message: "Password" })}
          name="password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={setPassword}
        />
        <Button variant="primary" type="submit">
          {t({ id: "login.password.submit", message: "Sign in" })}
        </Button>
      </form>
    </Card>
  );
}
