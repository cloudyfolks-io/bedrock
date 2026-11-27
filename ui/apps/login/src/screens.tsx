import type { ComponentType } from "react";
import { Username } from "./screens/Username";
import { SAxess } from "./screens/SAxess";
import { Password } from "./screens/Password";
import { Totp } from "./screens/Totp";
import { Recovery } from "./screens/Recovery";
import { TotpEnroll } from "./screens/TotpEnroll";
import { Providers } from "./screens/Providers";
import { DeviceConfirm } from "./screens/DeviceConfirm";
import { Done } from "./screens/Done";
import { ErrorScreen } from "./screens/ErrorScreen";
import type { Challenge, ChallengeType } from "./types";

export interface ScreenProps {
  challenge: Challenge;
  onChallenge: (next: Challenge) => void;
}

const registry: Record<ChallengeType, ComponentType<ScreenProps>> = {
  username: Username,
  saxess: SAxess,
  password: Password,
  totp: Totp,
  recovery: Recovery,
  "totp-enroll": TotpEnroll,
  providers: Providers,
  redirect: Done,
  "device-confirm": DeviceConfirm,
  done: Done,
  error: ErrorScreen,
};

export function screenFor(challenge: Challenge): ComponentType<ScreenProps> {
  return registry[challenge.type];
}
