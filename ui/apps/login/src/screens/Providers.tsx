import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import { useAnswer, useFocusOnError } from "../useAnswer";
import type { ScreenProps } from "../screens";

export function Providers({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();
  const { error, submit } = useAnswer(onChallenge);
  const errorRef = useFocusOnError(error !== null);

  return (
    <Card title={t({ id: "login.providers.title", message: "Choose how to sign in" })}>
      {error ? (
        <div ref={errorRef}>
          <Alert tone="error">{t(errorDescriptor(error))}</Alert>
        </div>
      ) : null}
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
