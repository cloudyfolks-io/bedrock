import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Button, Card, TextField } from "@bedrock/design";
import * as api from "../api";
import type { ScreenProps } from "../screens";

export function Username({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();
  const [username, setUsername] = useState("");

  return (
    <Card title={t({ id: "login.username.title", message: "Sign in" })}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          api.answer({ type: "username", username }, challenge.csrf ?? "").then(onChallenge);
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
            api.answer({ type: "providers", provider: provider.name }, challenge.csrf ?? "").then(onChallenge);
          }}
        >
          {provider.displayName}
        </Button>
      ))}
    </Card>
  );
}
