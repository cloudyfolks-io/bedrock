import { ApiError } from "../api";

export function handleAccountError(
  thrown: unknown,
  onSessionExpired: () => void,
  setError: (code: string) => void,
): void {
  if (thrown instanceof ApiError && thrown.code === "no_session") {
    onSessionExpired();
    return;
  }
  setError(thrown instanceof ApiError ? thrown.code : "unknown");
}
