import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, OtpField } from "@bedrock/design";
import * as api from "../api";
import { ApiError } from "../api";
import { errorDescriptor } from "../errorMessages";
import { otpauthSVG } from "../qr";
import type { AccountMethod } from "../types";
import { handleAccountError } from "./session";

interface TotpSectionProps {
  csrf: string;
  methods: AccountMethod[];
  onChanged: () => void;
  onSessionExpired: () => void;
}

export function TotpSection({ csrf, methods, onChanged, onSessionExpired }: TotpSectionProps) {
  const { t } = useLingui();
  const enrolled = methods.some((method) => method.method === "totp");
  const [enrollment, setEnrollment] = useState<{ secret: string; svg: string } | null>(null);
  const [code, setCode] = useState("");
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null);
  const [blocked, setBlocked] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const startEnroll = () => {
    setError(null);
    api
      .beginTOTP(csrf)
      .then((data) => otpauthSVG(data.otpauthURL).then((svg) => setEnrollment({ secret: data.secret, svg })))
      .catch((thrown: unknown) => handleAccountError(thrown, onSessionExpired, setError));
  };

  const verify = () => {
    setError(null);
    api
      .verifyTOTP(code, csrf)
      .then((result) => {
        setEnrollment(null);
        setCode("");
        setRecoveryCodes(result.recoveryCodes);
        onChanged();
      })
      .catch((thrown: unknown) => handleAccountError(thrown, onSessionExpired, setError));
  };

  const remove = () => {
    setBlocked(false);
    setError(null);
    api
      .removeTOTP(csrf)
      .then(onChanged)
      .catch((thrown: unknown) => {
        if (thrown instanceof ApiError && thrown.code === "second_factor_required") {
          setBlocked(true);
          return;
        }
        handleAccountError(thrown, onSessionExpired, setError);
      });
  };

  if (recoveryCodes) {
    return (
      <section>
        <h3>{t({ id: "account.totp.title", message: "Authenticator app" })}</h3>
        <p>{t({ id: "account.recovery.hint", message: "Each code works once. Store them somewhere safe." })}</p>
        <ul>
          {recoveryCodes.map((recoveryCode) => (
            <li key={recoveryCode}>{recoveryCode}</li>
          ))}
        </ul>
        <Button variant="secondary" onClick={() => navigator.clipboard.writeText(recoveryCodes.join("\n"))}>
          {t({ id: "account.recovery.copy", message: "Copy" })}
        </Button>
        <Button variant="primary" onClick={() => setRecoveryCodes(null)}>
          {t({ id: "account.done", message: "Done" })}
        </Button>
      </section>
    );
  }

  return (
    <section>
      <h3>{t({ id: "account.totp.title", message: "Authenticator app" })}</h3>
      <p>
        {enrolled
          ? t({ id: "account.totp.enrolled", message: "Enabled" })
          : t({ id: "account.totp.notEnrolled", message: "Not set up" })}
      </p>
      {blocked ? (
        <Alert tone="error">
          {t({ id: "account.totp.removeBlocked", message: "A second factor is required by policy and cannot be removed." })}
        </Alert>
      ) : error ? (
        <Alert tone="error">{t(errorDescriptor(error))}</Alert>
      ) : null}
      {enrolled ? (
        <Button variant="secondary" onClick={remove}>
          {t({ id: "account.totp.remove", message: "Remove" })}
        </Button>
      ) : enrollment ? (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            verify();
          }}
        >
          <div dangerouslySetInnerHTML={{ __html: enrollment.svg }} />
          <code>{enrollment.secret}</code>
          <OtpField
            length={6}
            value={code}
            onChange={setCode}
            label={t({ id: "account.totp.codeLabel", message: "Enter the 6-digit code" })}
          />
          <Button variant="primary" type="submit">
            {t({ id: "account.totp.verify", message: "Verify" })}
          </Button>
        </form>
      ) : (
        <Button variant="primary" onClick={startEnroll}>
          {t({ id: "account.totp.enroll", message: "Set up" })}
        </Button>
      )}
    </section>
  );
}
