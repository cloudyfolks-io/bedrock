import { useLingui } from "@lingui/react/macro";
import { Alert, Button } from "@bedrock/design";
import { isHttpError } from "../api/http";

export function QueryFailure({ error, retry }: { error: unknown; retry: () => void }) {
  const { t } = useLingui();
  if (isHttpError(error) && error.status === 403) {
    return <Alert tone="error">{t({ id: "list.forbidden.title", message: "You cannot view this" })}</Alert>;
  }
  return (
    <Alert tone="error">
      <p>{t({ id: "query.error", message: "The panel could not load this data." })}</p>
      <Button variant="secondary" onClick={retry}>{t({ id: "common.action.retry", message: "Retry" })}</Button>
    </Alert>
  );
}
