import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card, TextField } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import { useAnswer } from "../useAnswer";
import type { ScreenProps } from "../screens";

export function Password({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();
  const [password, setPassword] = useState("");
  const { error, submit } = useAnswer(onChallenge);

  return (
    <Card title={t({ id: "login.password.title", message: "Enter your password" })}>
      {error ? <Alert tone="error">{t(errorDescriptor(error))}</Alert> : null}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          submit(api.answer({ type: "password", password }, challenge.csrf ?? ""));
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
