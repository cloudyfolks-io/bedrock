import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card, TextField } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import { useAnswer, useFocusFirstField, useFocusOnError } from "../useAnswer";
import type { ScreenProps } from "../screens";

export function Password({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();
  const [password, setPassword] = useState("");
  const { error, submit } = useAnswer(onChallenge);
  const fieldRef = useFocusFirstField();
  const errorRef = useFocusOnError(error !== null);

  return (
    <Card title={t({ id: "login.password.title", message: "Enter your password" })}>
      {error ? (
        <div ref={errorRef}>
          <Alert tone="error">{t(errorDescriptor(error))}</Alert>
        </div>
      ) : null}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          submit(api.answer({ type: "password", password }, challenge.csrf ?? ""));
        }}
      >
        <div ref={fieldRef}>
          <TextField
            label={t({ id: "login.password.label", message: "Password" })}
            name="password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={setPassword}
          />
        </div>
        <Button variant="primary" type="submit">
          {t({ id: "login.password.submit", message: "Sign in" })}
        </Button>
      </form>
    </Card>
  );
}
