import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, TextField } from "@bedrock/design";
import * as api from "../api";
import { ApiError } from "../api";
import { errorDescriptor } from "../errorMessages";

interface PasswordSectionProps {
  csrf: string;
}

export function PasswordSection({ csrf }: PasswordSectionProps) {
  const { t } = useLingui();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [done, setDone] = useState(false);
  const [error, setError] = useState<string | null>(null);

  return (
    <section>
      <h3>{t({ id: "account.password.title", message: "Password" })}</h3>
      {done ? <Alert tone="info">{t({ id: "account.password.success", message: "Password changed" })}</Alert> : null}
      {error ? <Alert tone="error">{t(errorDescriptor(error))}</Alert> : null}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          setError(null);
          api
            .changePassword(current, next, csrf)
            .then(() => {
              setDone(true);
              setCurrent("");
              setNext("");
            })
            .catch((thrown: unknown) => {
              setError(thrown instanceof ApiError ? thrown.code : "unknown");
            });
        }}
      >
        <TextField
          label={t({ id: "account.password.current", message: "Current password" })}
          name="current"
          type="password"
          autoComplete="current-password"
          value={current}
          onChange={setCurrent}
        />
        <TextField
          label={t({ id: "account.password.new", message: "New password" })}
          name="new"
          type="password"
          autoComplete="new-password"
          value={next}
          onChange={setNext}
        />
        <Button variant="primary" type="submit">
          {t({ id: "account.password.submit", message: "Change password" })}
        </Button>
      </form>
    </section>
  );
}
