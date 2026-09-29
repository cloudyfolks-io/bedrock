import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card, TextField } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import { useAnswer } from "../useAnswer";
import type { ScreenProps } from "../screens";

export function Username({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();
  const [username, setUsername] = useState("");
  const { error, submit } = useAnswer(onChallenge);

  return (
    <Card title={t({ id: "login.username.title", message: "Sign in" })}>
      {error ? <Alert tone="error">{t(errorDescriptor(error))}</Alert> : null}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          submit(api.answer({ type: "username", username }, challenge.csrf ?? ""));
        }}
      >
        <TextField
          label={t({ id: "login.username.label", message: "Username" })}
          name="username"
          autoComplete="username"
          value={username}
          onChange={setUsername}
        />
        <Button variant="primary" type="submit">
          {t({ id: "login.username.continue", message: "Continue" })}
        </Button>
      </form>
      {(challenge.providers ?? []).map((provider) => (
        <Button
          key={provider.name}
          variant="secondary"
          onClick={() => {
            submit(api.answer({ type: "providers", provider: provider.name }, challenge.csrf ?? ""));
          }}
        >
          {provider.displayName}
        </Button>
      ))}
    </Card>
  );
}
