import { useLingui } from "@lingui/react/macro";
import { Card } from "@bedrock/design";
import type { ScreenProps } from "../screens";

export function Done({ challenge }: ScreenProps) {
  const { t } = useLingui();
  return <Card title={t({ id: "login.done", message: "Signed in. Redirecting…" })}>{challenge.redirect}</Card>;
}
