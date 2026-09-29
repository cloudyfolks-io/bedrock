export type ChallengeType =
  | "username"
  | "password"
  | "totp"
  | "recovery"
  | "totp-enroll"
  | "providers"
  | "redirect"
  | "device-confirm"
  | "done"
  | "error";

export interface ProviderChoice {
  name: string;
  displayName: string;
  type: "ldap" | "oidc";
}

export interface TOTPEnrollment {
  otpauthURL: string;
  secret: string;
}

export interface DeviceChallenge {
  userCode: string;
  clientID: string;
  scopes: string[];
}

export interface ChallengeError {
  code: string;
}

export interface Challenge {
  type: ChallengeType;
  csrf?: string;
  username?: string;
  methods?: string[];
  providers?: ProviderChoice[];
  redirect?: string;
  enroll?: TOTPEnrollment;
  recoveryCodes?: string[];
  device?: DeviceChallenge;
  error?: ChallengeError;
}

export interface Answer {
  type: string;
  username?: string;
  password?: string;
  code?: string;
  provider?: string;
  approve?: boolean;
  method?: string;
}

export interface AccountMethod {
  method: string;
  enrolledAt: string | null;
  lastUsed: string | null;
}

export interface Account {
  user: {
    name: string;
    username: string;
    displayName: string;
    email: string;
    source: string;
    groups: string[];
  };
  methods: AccountMethod[];
  csrf: string;
}

export interface TokenInfo {
  id: string;
  description: string;
  scopes: string[];
  expiresAt: string | null;
  lastUsed: string | null;
}

export interface SessionInfo {
  id: string;
  current: boolean;
  authTime: string;
  lastSeen: string | null;
  userAgent: string;
  clientIP: string;
}
