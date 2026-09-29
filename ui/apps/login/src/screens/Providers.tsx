import { useLingui } from "@lingui/react/macro";
import { Button, Card } from "@bedrock/design";
import * as api from "../api";
import type { ScreenProps } from "../screens";

export function Providers({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();

  return (
    <Card title={t({ id: "login.providers.title", message: "Choose how to sign in" })}>
      {(challenge.providers ?? []).map((provider) => (
        <Button
          key={provider.name}
          variant="secondary"
          onClick={() => {
            api.answer({ type: "providers", provider: provider.name }, challenge.csrf ?? "").then(onChallenge);
          }}
        >
          {provider.displayName}
        </Button>
      ))}
    </Card>
  );
}
