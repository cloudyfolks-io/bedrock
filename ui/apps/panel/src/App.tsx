import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, Outlet } from "react-router";
import { RouterProvider } from "react-router/dom";
import { ThemeProvider, ToastProvider } from "@bedrock/design";
import { HttpProvider } from "./api/HttpProvider";
import { SessionGate } from "./api/session";
import { AuthzProvider } from "./authz/authz";
import { PendingWrites } from "./components/PendingWrites";
import { LocaleProvider } from "./LocaleProvider";
import { panelRoutes } from "./shell/routes";
import { StreamProvider } from "./stream/StreamProvider";

function PanelSession() {
  return (
    <HttpProvider>
      <SessionGate>
        <ToastProvider>
          <StreamProvider>
            <AuthzProvider>
              <Outlet />
              <PendingWrites />
            </AuthzProvider>
          </StreamProvider>
        </ToastProvider>
      </SessionGate>
    </HttpProvider>
  );
}

const router = typeof window === "undefined" ? undefined : createBrowserRouter([
  { id: "panel-session", element: <PanelSession />, children: panelRoutes },
]);

export function App() {
  const [queryClient] = useState(() => new QueryClient());
  return (
    <QueryClientProvider client={queryClient}>
      <LocaleProvider>
        <ThemeProvider theme="dark">
          {router ? <RouterProvider router={router} /> : null}
        </ThemeProvider>
      </LocaleProvider>
    </QueryClientProvider>
  );
}
