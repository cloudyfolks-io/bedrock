import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card, TextField } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import { useAnswer, useFocusFirstField, useFocusOnError } from "../useAnswer";
import type { Challenge } from "../types";

interface DeviceEntryProps {
  onChallenge: (next: Challenge) => void;
}

export function DeviceEntry({ onChallenge }: DeviceEntryProps) {
  const { t } = useLingui();
  const [userCode, setUserCode] = useState("");
  const { error, submit } = useAnswer(onChallenge);
  const fieldRef = useFocusFirstField();
  const errorRef = useFocusOnError(error !== null);

  return (
    <Card title={t({ id: "login.device.enterCode", message: "Enter the code shown on your device" })}>
      {error ? (
        <div ref={errorRef}>
          <Alert tone="error">{t(errorDescriptor(error))}</Alert>
        </div>
      ) : null}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          submit(api.startDevice(userCode));
        }}
      >
        <div ref={fieldRef}>
          <TextField
            label={t({ id: "login.device.codeLabel", message: "Device code" })}
            name="userCode"
            autoComplete="off"
            value={userCode}
            onChange={setUserCode}
          />
        </div>
        <Button variant="primary" type="submit">
          {t({ id: "login.device.submit", message: "Continue" })}
        </Button>
      </form>
    </Card>
  );
}
