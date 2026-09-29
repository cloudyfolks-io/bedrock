import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card, OtpField } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import { useAnswer } from "../useAnswer";
import type { ScreenProps } from "../screens";

export function Totp({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();
  const [code, setCode] = useState("");
  const { error, submit } = useAnswer(onChallenge);

  return (
    <Card title={t({ id: "login.totp.title", message: "Enter your authenticator code" })}>
      {error ? <Alert tone="error">{t(errorDescriptor(error))}</Alert> : null}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          submit(api.answer({ type: "totp", code }, challenge.csrf ?? ""));
        }}
      >
        <OtpField
          length={6}
          value={code}
          onChange={setCode}
          label={t({ id: "login.totp.label", message: "Authentication code" })}
        />
        <Button variant="primary" type="submit">
          {t({ id: "login.totp.submit", message: "Verify" })}
        </Button>
      </form>
      <Button variant="secondary" onClick={() => onChallenge({ ...challenge, type: "recovery" })}>
        {t({ id: "login.totp.useRecovery", message: "Use a recovery code instead" })}
      </Button>
    </Card>
  );
}
