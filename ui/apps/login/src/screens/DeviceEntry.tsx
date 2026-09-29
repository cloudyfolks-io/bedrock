import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Button, Card, TextField } from "@bedrock/design";
import * as api from "../api";
import type { Challenge } from "../types";

interface DeviceEntryProps {
  onChallenge: (next: Challenge) => void;
}

export function DeviceEntry({ onChallenge }: DeviceEntryProps) {
  const { t } = useLingui();
  const [userCode, setUserCode] = useState("");

  return (
    <Card title={t({ id: "login.device.enterCode", message: "Enter the code shown on your device" })}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          api.startDevice(userCode).then(onChallenge);
        }}
      >
        <TextField
          label={t({ id: "login.device.codeLabel", message: "Device code" })}
          name="userCode"
          autoComplete="off"
          value={userCode}
          onChange={setUserCode}
        />
        <Button variant="primary" type="submit">
          {t({ id: "login.device.submit", message: "Continue" })}
        </Button>
      </form>
    </Card>
  );
}
