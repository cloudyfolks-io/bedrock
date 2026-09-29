import { useCallback, useEffect, useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Card } from "@bedrock/design";
import * as api from "../api";
import { loginPath } from "../router";
import type { Account as AccountData } from "../types";
import { PasswordSection } from "./PasswordSection";
import { RecoverySection } from "./RecoverySection";
import { SessionsSection } from "./SessionsSection";
import { TokensSection } from "./TokensSection";
import { TotpSection } from "./TotpSection";

export function Account() {
  const { t } = useLingui();
  const [account, setAccount] = useState<AccountData | null>(null);
  const [signedOut, setSignedOut] = useState(false);

  const load = useCallback(() => {
    api.account().then(setAccount).catch(() => setSignedOut(true));
  }, []);

  useEffect(load, [load]);

  if (signedOut) {
    return (
      <Card title={t({ id: "account.signedOut.title", message: "You are signed out" })}>
        <a href={loginPath()}>{t({ id: "account.signedOut.link", message: "Start a new sign-in" })}</a>
      </Card>
    );
  }

  if (!account) {
    return null;
  }

  return (
    <Card title={t({ id: "account.title", message: "Account" })}>
      {account.user.source === "" ? <PasswordSection csrf={account.csrf} /> : null}
      <TotpSection csrf={account.csrf} methods={account.methods} onChanged={load} />
      <RecoverySection csrf={account.csrf} />
      <TokensSection csrf={account.csrf} />
      <SessionsSection csrf={account.csrf} />
    </Card>
  );
}
