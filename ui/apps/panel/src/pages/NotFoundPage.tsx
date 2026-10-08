import { useLingui } from "@lingui/react/macro";
import { EmptyState } from "../components/EmptyState";

export function NotFoundPage() {
  const { t } = useLingui();
  return (
    <EmptyState
      title={t({ id: "shell.notFound.title", message: "Page not found" })}
      description={t({ id: "shell.notFound.body", message: "This page does not exist, or you cannot see it." })}
    />
  );
}
