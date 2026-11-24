import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card, TextField } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import { useAnswer, useFocusFirstField, useFocusOnError } from "../useAnswer";
import type { ScreenProps } from "../screens";

export function Username({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();
  const [username, setUsername] = useState("");
  const { error: answerError, submit } = useAnswer(onChallenge);
  const error = answerError ?? challenge.error?.code;
  const fieldRef = useFocusFirstField();
  const errorRef = useFocusOnError(Boolean(error));

  return (
    <Card title={t({ id: "login.username.title", message: "Sign in" })}>
      {error ? (
        <div ref={errorRef}>
          <Alert tone="error">{t(errorDescriptor(error))}</Alert>
        </div>
      ) : null}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          submit(api.answer({ type: "username", username }, challenge.csrf ?? ""));
        }}
      >
        <div ref={fieldRef}>
          <TextField
            label={t({ id: "login.username.label", message: "Username" })}
            name="username"
            autoComplete="username"
            value={username}
            onChange={setUsername}
          />
        </div>
        <Button variant="primary" type="submit">
          {t({ id: "login.username.continue", message: "Continue" })}
        </Button>
        {challenge.saxess ? (
          <Button
            variant="secondary"
            type="button"
            disabled={!username.trim()}
            onClick={() => {
              submit(api.answer({ type: "saxess-start", username }, challenge.csrf ?? ""));
            }}
          >
            {t({ id: "login.saxess.start", message: "Sign in with sAxess Pro" })}
          </Button>
        ) : null}
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
