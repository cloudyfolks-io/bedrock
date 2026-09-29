import { useEffect, useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Card, OtpField } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import { otpauthSVG } from "../qr";
import { useAnswer, useFocusFirstField, useFocusOnError } from "../useAnswer";
import type { ScreenProps } from "../screens";

export function TotpEnroll({ challenge, onChallenge }: ScreenProps) {
  const { t } = useLingui();
  const [svg, setSvg] = useState("");
  const [code, setCode] = useState("");
  const { error, submit, fail } = useAnswer(onChallenge);
  const fieldRef = useFocusFirstField();
  const errorRef = useFocusOnError(error !== null);
  const codes = challenge.recoveryCodes;

  useEffect(() => {
    if (challenge.enroll) {
      otpauthSVG(challenge.enroll.otpauthURL).then(setSvg).catch(fail);
    }
  }, [challenge.enroll]);

  const errorAlert = error ? (
    <div ref={errorRef}>
      <Alert tone="error">{t(errorDescriptor(error))}</Alert>
    </div>
  ) : null;

  if (codes) {
    return (
      <Card title={t({ id: "login.enroll.recoveryCodes", message: "Save these recovery codes" })}>
        {errorAlert}
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
          onClick={() => submit(api.answer({ type: "totp-enroll" }, challenge.csrf ?? ""))}
        >
          {t({ id: "login.enroll.continue", message: "Continue" })}
        </Button>
      </Card>
    );
  }

  return (
    <Card title={t({ id: "login.enroll.title", message: "Set up your authenticator" })}>
      {errorAlert}
      <p>{t({ id: "login.enroll.scan", message: "Scan this code with your authenticator app" })}</p>
      <div dangerouslySetInnerHTML={{ __html: svg }} />
      <p>{t({ id: "login.enroll.secretLabel", message: "Or enter this key manually" })}</p>
      <code>{challenge.enroll?.secret}</code>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          submit(api.answer({ type: "totp-enroll", code }, challenge.csrf ?? ""));
        }}
      >
        <div ref={fieldRef}>
          <OtpField
            length={6}
            value={code}
            onChange={setCode}
            label={t({ id: "login.enroll.codeLabel", message: "Enter the 6-digit code" })}
          />
        </div>
        <Button variant="primary" type="submit">
          {t({ id: "login.enroll.submit", message: "Verify" })}
        </Button>
      </form>
    </Card>
  );
}
