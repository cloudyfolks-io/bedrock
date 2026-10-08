import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { ToastProvider } from "@bedrock/design";
import { ThemeChoiceProvider } from "../theme";
import { HttpProvider, sessionKey } from "../api/HttpProvider";
import type { Session } from "../api/types";
import type { Locale } from "../i18n";
import { LocaleProvider } from "../LocaleProvider";
import { LocationProvider } from "../location";
import { AuthzProvider } from "../authz/authz";
import { StreamProvider } from "../stream/StreamProvider";
import type { Check } from "../api/types";
import { createFakeApi, type FakeApi, type FakeRoute } from "./fakeApi";
import { createFakeSockets, type FakeSockets } from "./fakeSocket";

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
  socket?: FakeSockets;
  allow?: (check: Check) => boolean;
}

export interface PanelRender extends Omit<ReturnType<typeof render>, "rerender"> {
  rerender: (node: ReactElement) => void;
  api: FakeApi;
  sockets: FakeSockets;
  queryClient: QueryClient;
}

export function renderPanel(node: ReactElement, options: RenderOptions = {}): PanelRender {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const api = createFakeApi(options.api ?? {}, options.allow ?? (() => true));
  const sockets = options.socket ?? createFakeSockets();
  api.install();
  if (options.session !== null) {
    queryClient.setQueryData(sessionKey, { ...defaultSession, ...options.session });
  }
  const assign = options.assign ?? (() => {});
  const wrap = (content: ReactElement) => (
    <QueryClientProvider client={queryClient}>
      <LocaleProvider initial={options.locale ?? "en"}>
        <ThemeChoiceProvider>
          <ToastProvider>
            <MemoryRouter initialEntries={[options.route ?? "/"]}>
              <LocationProvider actions={{ assign }}>
                <HttpProvider>
                  <StreamProvider createSocket={sockets.create}>
                    <AuthzProvider>{content}</AuthzProvider>
                  </StreamProvider>
                </HttpProvider>
              </LocationProvider>
            </MemoryRouter>
          </ToastProvider>
        </ThemeChoiceProvider>
      </LocaleProvider>
    </QueryClientProvider>
  );
  const result = render(wrap(node));
  return { ...result, rerender: (next: ReactElement) => result.rerender(wrap(next)), api, sockets, queryClient };
}
