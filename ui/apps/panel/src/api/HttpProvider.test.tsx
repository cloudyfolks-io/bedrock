import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { createFakeApi } from "../test/fakeApi";
import { defaultSession } from "../test/renderPanel";
import { HttpProvider, sessionKey, useHttp } from "./HttpProvider";
import type { Http } from "./http";

describe("HttpProvider", () => {
  it("resets the unauthenticated guard when the session query succeeds", async () => {
    const api = createFakeApi({ "GET /a": { status: 401, body: { error: "unauthenticated" } } });
    api.install();
    const queryClient = new QueryClient();
    queryClient.setQueryData(sessionKey, defaultSession);
    let http: Http | undefined;
    function Capture() {
      http = useHttp();
      return null;
    }
    render(
      <QueryClientProvider client={queryClient}>
        <HttpProvider>
          <Capture />
        </HttpProvider>
      </QueryClientProvider>,
    );
    await http?.request("GET", "/a").catch(() => undefined);
    await waitFor(() => expect(queryClient.getQueryState(sessionKey)?.isInvalidated).toBe(true));
    queryClient.setQueryData(sessionKey, defaultSession);
    await waitFor(() => expect(queryClient.getQueryState(sessionKey)?.isInvalidated).toBe(false));
    await http?.request("GET", "/a").catch(() => undefined);
    await waitFor(() => expect(queryClient.getQueryState(sessionKey)?.isInvalidated).toBe(true));
  });
});
