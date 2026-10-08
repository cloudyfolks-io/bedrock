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

export const sessionKeys = {
  signin: "bedrock.panel.signin",
} as const;

export type SessionName = keyof typeof sessionKeys;

export function readSession(name: SessionName): string | null {
  try {
    return sessionStorage.getItem(sessionKeys[name]);
  } catch {
    return null;
  }
}

export function writeSession(name: SessionName, value: string): void {
  try {
    sessionStorage.setItem(sessionKeys[name], value);
  } catch {
    return;
  }
}

export function removeSession(name: SessionName): void {
  try {
    sessionStorage.removeItem(sessionKeys[name]);
  } catch {
    return;
  }
}
