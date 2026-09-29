import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import { useAnswer } from "../useAnswer";
import type { ScreenProps } from "../screens";

export function DeviceConfirm({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();
  const { error, submit } = useAnswer(onChallenge);

  const respond = (approve: boolean) => {
    submit(api.answer({ type: "device-confirm", approve }, challenge.csrf ?? ""));
  };

  return (
    <Card title={t({ id: "login.device.confirm", message: "Confirm sign-in for this device?" })}>
      {error ? <Alert tone="error">{t(errorDescriptor(error))}</Alert> : null}
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
