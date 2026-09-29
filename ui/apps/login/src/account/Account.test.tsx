import { screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import { dirOf, locales } from "../i18n";
import { expectedMessage } from "../test/expectedMessage";
import { renderWithLocale } from "../test/renderWithLocale";
import { Account } from "./Account";

vi.mock("../api", () => ({
  account: vi.fn(),
  changePassword: vi.fn(),
  beginTOTP: vi.fn(),
  verifyTOTP: vi.fn(),
  removeTOTP: vi.fn(),
  newRecoveryCodes: vi.fn(),
  listTokens: vi.fn().mockResolvedValue([]),
  createToken: vi.fn(),
  revokeToken: vi.fn(),
  listSessions: vi.fn().mockResolvedValue([]),
  revokeSession: vi.fn(),
  logout: vi.fn(),
  ApiError: class ApiError extends Error {
    code: string;
    constructor(code: string) {
      super(code);
      this.code = code;
    }
  },
}));

const account = {
  user: { name: "alice", username: "alice", displayName: "Alice", email: "alice@bedrock.test", source: "", groups: [] },
  methods: [],
  csrf: "csrf-acct",
};

describe("Account", () => {
  beforeEach(() => {
    vi.mocked(api.account).mockReset();
  });

  for (const locale of locales) {
    it(`renders the account page in ${locale}`, async () => {
      vi.mocked(api.account).mockResolvedValue(account);
      renderWithLocale(<Account />, locale);
      expect(await screen.findByText(expectedMessage(locale, "account.title"))).not.toBeNull();
      expect(document.documentElement.dir).toBe(dirOf(locale));
    });

    it(`shows a sign-in link in ${locale} on a failed fetch`, async () => {
      vi.mocked(api.account).mockRejectedValue(new Error("unauthenticated"));
      renderWithLocale(<Account />, locale);
      expect(await screen.findByText(expectedMessage(locale, "account.signedOut.title"))).not.toBeNull();
      const link = screen.getByText(expectedMessage(locale, "account.signedOut.link")) as HTMLAnchorElement;
      expect(link.getAttribute("href")).toBe("/login/");
    });
  }
});
