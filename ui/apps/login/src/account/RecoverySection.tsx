import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button } from "@bedrock/design";
import * as api from "../api";
import { ApiError } from "../api";
import { errorDescriptor } from "../errorMessages";

interface RecoverySectionProps {
  csrf: string;
}

export function RecoverySection({ csrf }: RecoverySectionProps) {
  const { t } = useLingui();
  const [codes, setCodes] = useState<string[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const generate = () => {
    setError(null);
    api
      .newRecoveryCodes(csrf)
      .then((result) => setCodes(result.recoveryCodes))
      .catch((thrown: unknown) => {
        setError(thrown instanceof ApiError ? thrown.code : "unknown");
      });
  };

  if (codes) {
    return (
      <section>
        <h3>{t({ id: "account.recovery.title", message: "Recovery codes" })}</h3>
        <p>{t({ id: "account.recovery.hint", message: "Each code works once. Store them somewhere safe." })}</p>
        <ul>
          {codes.map((code) => (
            <li key={code}>{code}</li>
          ))}
        </ul>
        <Button variant="secondary" onClick={() => navigator.clipboard.writeText(codes.join("\n"))}>
          {t({ id: "account.recovery.copy", message: "Copy" })}
        </Button>
        <Button variant="primary" onClick={() => setCodes(null)}>
          {t({ id: "account.done", message: "Done" })}
        </Button>
      </section>
    );
  }

  return (
    <section>
      <h3>{t({ id: "account.recovery.title", message: "Recovery codes" })}</h3>
      {error ? <Alert tone="error">{t(errorDescriptor(error))}</Alert> : null}
      <Button variant="primary" onClick={generate}>
        {t({ id: "account.recovery.generate", message: "Generate new recovery codes" })}
      </Button>
    </section>
  );
}
