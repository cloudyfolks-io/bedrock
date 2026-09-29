import { readFileSync, writeFileSync } from "node:fs";

export interface ColorToken {
  light: string;
  dark: string;
}

export interface FontTokens {
  family: string;
  sizeSm: string;
  sizeMd: string;
  sizeLg: string;
  weightRegular: string;
  weightMedium: string;
  weightBold: string;
}

export interface Tokens {
  color: Record<string, ColorToken>;
  space: Record<string, string>;
  radius: Record<string, string>;
  font: FontTokens;
}

function cssName(name: string): string {
  return name.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`);
}

function colorProperties(tokens: Tokens, theme: "light" | "dark"): string {
  return Object.entries(tokens.color)
    .map(([name, value]) => `  --br-color-${cssName(name)}: ${value[theme]};`)
    .join("\n");
}

function scaleProperties(prefix: string, scale: Record<string, string>): string {
  return Object.entries(scale)
    .map(([name, value]) => `  --br-${prefix}-${cssName(name)}: ${value};`)
    .join("\n");
}

const componentRules = `
.br-button {
  border-radius: var(--br-radius-md);
  padding-block: var(--br-space-sm);
  padding-inline: var(--br-space-md);
  font-family: var(--br-font-family);
  font-size: var(--br-font-size-md);
  font-weight: var(--br-font-weight-medium);
  border-style: solid;
  border-width: 1px;
  cursor: pointer;
}

.br-button--primary {
  background-color: var(--br-color-primary);
  color: var(--br-color-primary-text);
  border-color: var(--br-color-primary);
}

.br-button--secondary {
  background-color: transparent;
  color: var(--br-color-text);
  border-color: var(--br-color-border);
}

.br-field {
  display: flex;
  flex-direction: column;
  gap: var(--br-space-xs);
}

.br-field-label {
  font-family: var(--br-font-family);
  font-size: var(--br-font-size-sm);
  color: var(--br-color-text-muted);
}

.br-field-input {
  border-radius: var(--br-radius-sm);
  border-style: solid;
  border-width: 1px;
  border-color: var(--br-color-border);
  background-color: var(--br-color-surface);
  color: var(--br-color-text);
  font-size: var(--br-font-size-md);
  padding-block: var(--br-space-sm);
  padding-inline: var(--br-space-sm);
}

.br-field-error {
  color: var(--br-color-danger);
  font-size: var(--br-font-size-sm);
}

.br-otp {
  display: flex;
  gap: var(--br-space-sm);
  border-style: none;
  padding: 0;
  margin: 0;
}

.br-otp-digit {
  inline-size: 2.5rem;
  block-size: 2.75rem;
  text-align: center;
  font-size: var(--br-font-size-lg);
  border-radius: var(--br-radius-sm);
  border-style: solid;
  border-width: 1px;
  border-color: var(--br-color-border);
  background-color: var(--br-color-surface);
  color: var(--br-color-text);
}

.br-alert {
  border-radius: var(--br-radius-md);
  padding-block: var(--br-space-sm);
  padding-inline: var(--br-space-md);
  font-size: var(--br-font-size-md);
}

.br-alert--error {
  background-color: var(--br-color-danger-surface);
  color: var(--br-color-danger);
}

.br-alert--info {
  background-color: var(--br-color-surface);
  color: var(--br-color-text);
}

.br-card {
  border-radius: var(--br-radius-lg);
  background-color: var(--br-color-surface);
  padding: var(--br-space-lg);
}

.br-card-title {
  font-family: var(--br-font-family);
  font-size: var(--br-font-size-lg);
  font-weight: var(--br-font-weight-bold);
  color: var(--br-color-text);
  margin-block-end: var(--br-space-md);
}

.br-language-menu-trigger {
  border-radius: var(--br-radius-sm);
  border-style: solid;
  border-width: 1px;
  border-color: var(--br-color-border);
  background-color: transparent;
  color: var(--br-color-text);
  padding-block: var(--br-space-xs);
  padding-inline: var(--br-space-sm);
  cursor: pointer;
}

.br-language-menu-popup {
  border-radius: var(--br-radius-sm);
  border-style: solid;
  border-width: 1px;
  border-color: var(--br-color-border);
  background-color: var(--br-color-surface);
  padding: var(--br-space-xs);
}

.br-language-menu-item {
  padding-block: var(--br-space-xs);
  padding-inline: var(--br-space-sm);
  color: var(--br-color-text);
  cursor: pointer;
}
`;

export function renderTokens(tokens: Tokens): string {
  return `:root {
${colorProperties(tokens, "light")}
${scaleProperties("space", tokens.space)}
${scaleProperties("radius", tokens.radius)}
  --br-font-family: ${tokens.font.family};
${scaleProperties("font", { "size-sm": tokens.font.sizeSm, "size-md": tokens.font.sizeMd, "size-lg": tokens.font.sizeLg, "weight-regular": tokens.font.weightRegular, "weight-medium": tokens.font.weightMedium, "weight-bold": tokens.font.weightBold }).replace(/--br-font-size-size-/g, "--br-font-size-").replace(/--br-font-weight-weight-/g, "--br-font-weight-")}
}

@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
${colorProperties(tokens, "dark")}
  }
}

:root[data-theme="dark"] {
${colorProperties(tokens, "dark")}
}
${componentRules}`;
}

function main(): void {
  const tokens = JSON.parse(readFileSync(new URL("../tokens.json", import.meta.url), "utf8")) as Tokens;
  writeFileSync(new URL("../src/tokens.css", import.meta.url), renderTokens(tokens));
}

if (process.argv[1]?.endsWith("tokens.ts")) {
  main();
}
