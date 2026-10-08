export const storedKeys = {
  locale: "bedrock.panel.locale",
  theme: "bedrock.panel.theme",
  perspective: "bedrock.panel.perspective",
  project: "bedrock.panel.project",
} as const;

export type StoredName = keyof typeof storedKeys;

export function readStored(name: StoredName): string | null {
  try {
    return localStorage.getItem(storedKeys[name]);
  } catch {
    return null;
  }
}

export function writeStored(name: StoredName, value: string): void {
  try {
    localStorage.setItem(storedKeys[name], value);
  } catch {
    return;
  }
}
