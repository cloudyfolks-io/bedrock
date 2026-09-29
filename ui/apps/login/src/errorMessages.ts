import type { MessageDescriptor } from "@lingui/core";

const catalog: Record<string, MessageDescriptor> = {
  invalid_credentials: { id: "login.error.invalid_credentials", message: "That username or password is not correct." },
  locked: { id: "login.error.locked", message: "This account is temporarily locked. Try again later." },
  rate_limited: { id: "login.error.rate_limited", message: "Too many attempts. Wait a moment and try again." },
  invalid_code: { id: "login.error.invalid_code", message: "That code is not correct." },
  provider_error: { id: "login.error.provider_error", message: "The identity provider could not complete sign-in." },
  disabled: { id: "login.error.disabled", message: "This account is disabled." },
};

export function errorDescriptor(code: string): MessageDescriptor {
  return catalog[code] ?? { id: "login.error.unknown", message: "Something went wrong. Try again." };
}
