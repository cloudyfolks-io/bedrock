import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card, TextField } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import { useAnswer } from "../useAnswer";
import type { ScreenProps } from "../screens";

export function Recovery({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();
  const [code, setCode] = useState("");
  const { error, submit } = useAnswer(onChallenge);

  return (
    <Card title={t({ id: "login.recovery.title", message: "Enter a recovery code" })}>
      {error ? <Alert tone="error">{t(errorDescriptor(error))}</Alert> : null}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          submit(api.answer({ type: "recovery", code, method: "recovery" }, challenge.csrf ?? ""));
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
