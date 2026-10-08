import { msg } from "@lingui/core/macro";
import type { MessageDescriptor } from "@lingui/core";

export interface FieldProblem {
  field: string;
  message: MessageDescriptor;
}

export type SpecValidator = (spec: Record<string, unknown>) => FieldProblem[];

const cronField = /^[0-9A-Za-z*,/-]+$/;

export function isCron(text: string): boolean {
  const fields = text.trim().split(/\s+/);
  return fields.length === 5 && fields.every((field) => cronField.test(field));
}

export function validateBackup(spec: Record<string, unknown>): FieldProblem[] {
  const schedule = spec.schedule;
  return typeof schedule === "string" && schedule !== "" && !isCron(schedule)
    ? [{ field: "spec.schedule", message: msg({ id: "validate.cron", message: "Use a cron schedule with five fields, for example 0 2 * * *" }) }]
    : [];
}

const keyPrefixes = ["ssh-ed25519", "ssh-rsa", "ecdsa-sha2-", "sk-ssh-ed25519", "sk-ecdsa-sha2-"];

export function validateSshKey(spec: Record<string, unknown>): FieldProblem[] {
  const key = typeof spec.publicKey === "string" ? spec.publicKey.trim() : "";
  if (key.includes("PRIVATE KEY")) {
    return [{ field: "spec.publicKey", message: msg({ id: "validate.privateKey", message: "This is a private key. Never paste it here. Paste the public key." }) }];
  }
  return keyPrefixes.some((prefix) => key.startsWith(prefix))
    ? []
    : [{ field: "spec.publicKey", message: msg({ id: "validate.publicKey", message: "A public key starts with ssh-ed25519, ssh-rsa or ecdsa-sha2-" }) }];
}
