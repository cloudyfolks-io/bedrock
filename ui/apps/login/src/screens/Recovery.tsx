import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card, TextField } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import { useAnswer, useFocusFirstField, useFocusOnError } from "../useAnswer";
import type { ScreenProps } from "../screens";

export function Recovery({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();
  const [code, setCode] = useState("");
  const { error, submit } = useAnswer(onChallenge);
  const fieldRef = useFocusFirstField();
  const errorRef = useFocusOnError(error !== null);

  return (
    <Card title={t({ id: "login.recovery.title", message: "Enter a recovery code" })}>
      {error ? (
        <div ref={errorRef}>
          <Alert tone="error">{t(errorDescriptor(error))}</Alert>
        </div>
      ) : null}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          submit(api.answer({ type: "recovery", code, method: "recovery" }, challenge.csrf ?? ""));
        }}
      >
        <div ref={fieldRef}>
          <TextField
            label={t({ id: "login.recovery.label", message: "Recovery code" })}
            name="recoveryCode"
            autoComplete="off"
            value={code}
            onChange={setCode}
          />
        </div>
        <Button variant="primary" type="submit">
          {t({ id: "login.recovery.submit", message: "Verify" })}
        </Button>
      </form>
    </Card>
  );
}
