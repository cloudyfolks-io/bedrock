import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card } from "@bedrock/design";
import { errorDescriptor } from "../errorMessages";
import type { ScreenProps } from "../screens";

export function ErrorScreen({ challenge }: ScreenProps) {
  const { t } = useLingui();
  const code = challenge.error?.code ?? "unknown";
  return (
    <Card title={t({ id: "login.error.title", message: "Sign-in failed" })}>
      <Alert tone="error">{t(errorDescriptor(code))}</Alert>
      <Button variant="primary" onClick={() => window.location.assign("/login/")}>
        {t({ id: "login.error.retry", message: "Try again" })}
      </Button>
    </Card>
  );
}
