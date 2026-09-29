import { useEffect, useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, TextField } from "@bedrock/design";
import * as api from "../api";
import { errorDescriptor } from "../errorMessages";
import type { TokenInfo } from "../types";
import { handleAccountError } from "./session";

interface TokensSectionProps {
  csrf: string;
  onSessionExpired: () => void;
}

export function TokensSection({ csrf, onSessionExpired }: TokensSectionProps) {
  const { t } = useLingui();
  const [tokens, setTokens] = useState<TokenInfo[]>([]);
  const [description, setDescription] = useState("");
  const [created, setCreated] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refresh = () => {
    api.listTokens().then(setTokens).catch((thrown: unknown) => handleAccountError(thrown, onSessionExpired, setError));
  };

  useEffect(refresh, []);

  return (
    <section>
      <h3>{t({ id: "account.tokens.title", message: "API tokens" })}</h3>
      {error ? <Alert tone="error">{t(errorDescriptor(error))}</Alert> : null}
      {created ? (
        <div>
          <p>{t({ id: "account.tokens.createdHint", message: "Copy this token now. You will not see it again." })}</p>
          <code>{created}</code>
          <Button variant="secondary" onClick={() => navigator.clipboard.writeText(created)}>
            {t({ id: "account.tokens.copy", message: "Copy" })}
          </Button>
          <Button variant="primary" onClick={() => setCreated(null)}>
            {t({ id: "account.done", message: "Done" })}
          </Button>
        </div>
      ) : (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            setError(null);
            api
              .createToken(description, [], null, csrf)
              .then((result) => {
                setCreated(result.token);
                setDescription("");
                refresh();
              })
              .catch((thrown: unknown) => handleAccountError(thrown, onSessionExpired, setError));
          }}
        >
          <TextField
            label={t({ id: "account.tokens.descriptionLabel", message: "Description" })}
            name="description"
            value={description}
            onChange={setDescription}
          />
          <Button variant="primary" type="submit">
            {t({ id: "account.tokens.create", message: "Create token" })}
          </Button>
        </form>
      )}
      {tokens.length === 0 ? (
        <p>{t({ id: "account.tokens.empty", message: "No tokens yet" })}</p>
      ) : (
        <ul>
          {tokens.map((token) => (
            <li key={token.id}>
              {token.description}
              <Button
                variant="secondary"
                onClick={() => {
                  setError(null);
                  api
                    .revokeToken(token.id, csrf)
                    .then(refresh)
                    .catch((thrown: unknown) => handleAccountError(thrown, onSessionExpired, setError));
                }}
              >
                {t({ id: "account.tokens.revoke", message: "Revoke" })}
              </Button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
