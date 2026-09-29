import { describe, expect, it } from "vitest";
import fixtures from "./fixtures/challenges.json";
import { screenFor } from "./screens";
import { Username } from "./screens/Username";
import { Password } from "./screens/Password";
import { Totp } from "./screens/Totp";
import { Recovery } from "./screens/Recovery";
import { TotpEnroll } from "./screens/TotpEnroll";
import { Providers } from "./screens/Providers";
import { DeviceConfirm } from "./screens/DeviceConfirm";
import { Done } from "./screens/Done";
import { ErrorScreen } from "./screens/ErrorScreen";
import type { Challenge } from "./types";

const expected: Record<string, unknown> = {
  username: Username,
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

describe("screenFor", () => {
  for (const fixture of fixtures as Array<{ name: string; challenge: Challenge }>) {
    it(`maps ${fixture.name} to its component`, () => {
      expect(screenFor(fixture.challenge)).toBe(expected[fixture.challenge.type]);
    });
  }
});
