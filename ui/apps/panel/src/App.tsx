import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter } from "react-router";
import { ToastProvider } from "@bedrock/design";
import { HttpProvider } from "./api/HttpProvider";
import { SessionGate } from "./api/session";
import { AuthzProvider } from "./authz/authz";
import { LocaleProvider } from "./LocaleProvider";
import { AppRoutes } from "./shell/routes";
import { StreamProvider } from "./stream/StreamProvider";
import { ThemeChoiceProvider } from "./theme";

export function App() {
  const [queryClient] = useState(() => new QueryClient());
  return (
    <QueryClientProvider client={queryClient}>
      <LocaleProvider>
        <ThemeChoiceProvider>
          <ToastProvider>
            <BrowserRouter>
              <HttpProvider>
                <SessionGate>
                  <StreamProvider>
                    <AuthzProvider>
                      <AppRoutes />
                    </AuthzProvider>
                  </StreamProvider>
                </SessionGate>
              </HttpProvider>
            </BrowserRouter>
          </ToastProvider>
        </ThemeChoiceProvider>
      </LocaleProvider>
    </QueryClientProvider>
  );
}
