import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { ThemeProvider, ToastProvider } from "@bedrock/design";
import { LocaleProvider } from "../LocaleProvider";
import type { Locale } from "../i18n";

export interface RenderOptions {
  locale?: Locale;
  route?: string;
}

export function renderPanel(node: ReactElement, options: RenderOptions = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial={options.locale ?? "en"}>
        <ThemeProvider>
          <ToastProvider>
            <MemoryRouter initialEntries={[options.route ?? "/"]}>{node}</MemoryRouter>
          </ToastProvider>
        </ThemeProvider>
      </LocaleProvider>
    </QueryClientProvider>,
  );
}
