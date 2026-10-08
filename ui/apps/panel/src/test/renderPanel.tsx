import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { ThemeProvider, ToastProvider } from "@bedrock/design";
import { HttpProvider, sessionKey } from "../api/HttpProvider";
import type { Session } from "../api/types";
import type { Locale } from "../i18n";
import { LocaleProvider } from "../LocaleProvider";
import { LocationProvider } from "../location";
import { createFakeApi, type FakeApi, type FakeRoute } from "./fakeApi";

export const defaultSession: Session = {
  user: { name: "admin", displayName: "Administrator", email: "admin@example.test", groups: ["bedrock-admins"] },
  admin: true,
  csrf: "csrf-token-for-tests-0123456789abcdefghijklm",
  expiresAt: "2026-10-09T00:00:00Z",
  host: "e2e.bedrock.test",
};

export interface RenderOptions {
  locale?: Locale;
  route?: string;
  session?: Partial<Session> | null;
  api?: Record<string, FakeRoute>;
  assign?: (url: string) => void;
}

export interface PanelRender extends ReturnType<typeof render> {
  api: FakeApi;
  queryClient: QueryClient;
}

export function renderPanel(node: ReactElement, options: RenderOptions = {}): PanelRender {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const api = createFakeApi(options.api ?? {});
  api.install();
  if (options.session !== null) {
    queryClient.setQueryData(sessionKey, { ...defaultSession, ...options.session });
  }
  const assign = options.assign ?? (() => {});
  const result = render(
    <QueryClientProvider client={queryClient}>
      <LocaleProvider initial={options.locale ?? "en"}>
        <ThemeProvider>
          <ToastProvider>
            <MemoryRouter initialEntries={[options.route ?? "/"]}>
              <LocationProvider actions={{ assign }}>
                <HttpProvider>{node}</HttpProvider>
              </LocationProvider>
            </MemoryRouter>
          </ToastProvider>
        </ThemeProvider>
      </LocaleProvider>
    </QueryClientProvider>,
  );
  return { ...result, api, queryClient };
}
