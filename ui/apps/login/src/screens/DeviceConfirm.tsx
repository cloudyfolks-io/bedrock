import { useLingui } from "@lingui/react/macro";
import { Button, Card } from "@bedrock/design";
import * as api from "../api";
import type { ScreenProps } from "../screens";

export function DeviceConfirm({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();

  const respond = (approve: boolean) => {
    api.answer({ type: "device-confirm", approve }, challenge.csrf ?? "").then(onChallenge);
  };

  return (
    <Card title={t({ id: "login.device.confirm", message: "Confirm sign-in for this device?" })}>
      <p>{challenge.device?.userCode}</p>
      <Button variant="primary" onClick={() => respond(true)}>
        {t({ id: "login.device.approve", message: "Approve" })}
      </Button>
      <Button variant="secondary" onClick={() => respond(false)}>
        {t({ id: "login.device.deny", message: "Deny" })}
      </Button>
    </Card>
  );
}
