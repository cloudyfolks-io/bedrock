import { useEffect, useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card, OtpField } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import { otpauthSVG } from "../qr";
import type { ScreenProps } from "../screens";

export function TotpEnroll({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();
  const [svg, setSvg] = useState("");
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const codes = challenge.recoveryCodes;

  useEffect(() => {
    if (challenge.enroll) {
      otpauthSVG(challenge.enroll.otpauthURL).then(setSvg);
    }
  }, [challenge.enroll]);

  if (codes) {
    return (
      <Card title={t({ id: "login.enroll.recoveryCodes", message: "Save these recovery codes" })}>
        <p>{t({ id: "login.enroll.recoveryCodesHint", message: "Each code works once. Store them somewhere safe." })}</p>
        <ul>
          {codes.map((recoveryCode) => (
            <li key={recoveryCode}>{recoveryCode}</li>
          ))}
        </ul>
        <Button variant="secondary" onClick={() => navigator.clipboard.writeText(codes.join("\n"))}>
          {t({ id: "login.enroll.copy", message: "Copy" })}
        </Button>
        <Button
          variant="primary"
          onClick={() => api.answer({ type: "totp-enroll" }, challenge.csrf ?? "").then(onChallenge)}
        >
          {t({ id: "login.enroll.continue", message: "Continue" })}
        </Button>
      </Card>
    );
  }

  return (
    <Card title={t({ id: "login.enroll.title", message: "Set up your authenticator" })}>
      {error ? <Alert tone="error">{t(errorDescriptor(error))}</Alert> : null}
      <p>{t({ id: "login.enroll.scan", message: "Scan this code with your authenticator app" })}</p>
      <div dangerouslySetInnerHTML={{ __html: svg }} />
      <p>{t({ id: "login.enroll.secretLabel", message: "Or enter this key manually" })}</p>
      <code>{challenge.enroll?.secret}</code>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          api
            .answer({ type: "totp-enroll", code }, challenge.csrf ?? "")
            .then((next) => {
              if (next.type === "error") {
                setError(next.error?.code ?? "unknown");
                return;
              }
              onChallenge(next);
            })
            .catch(() => setError("unknown"));
        }}
      >
        <OtpField
          length={6}
          value={code}
          onChange={setCode}
          label={t({ id: "login.enroll.codeLabel", message: "Enter the 6-digit code" })}
        />
        <Button variant="primary" type="submit">
          {t({ id: "login.enroll.submit", message: "Verify" })}
        </Button>
      </form>
    </Card>
  );
}
