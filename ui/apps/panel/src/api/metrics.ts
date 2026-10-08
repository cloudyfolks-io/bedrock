import { useQueries } from "@tanstack/react-query";
import { useHttp } from "./HttpProvider";
import { isHttpError } from "./http";

export type Range = "1h" | "6h" | "24h" | "7d";

export const ranges: Record<Range, number> = { "1h": 3600, "6h": 21600, "24h": 86400, "7d": 604800 };

export interface MatrixSeries {
  metric: Record<string, string>;
  values: [number, string][];
}

export interface MatrixResult {
  status: string;
  data: { resultType: string; result: MatrixSeries[] };
}

export interface MetricOutcome {
  unavailable: boolean;
  forbidden: boolean;
  result: MatrixResult | undefined;
}

const steps = [15, 30, 60, 120, 300, 600, 900, 1800, 3600];
const maxPoints = 1500;

export function stepFor(rangeSeconds: number): number {
  return steps.find((step) => rangeSeconds / step <= maxPoints) ?? 3600;
}

export interface MetricParams {
  query: string;
  namespace?: string | undefined;
  name?: string | undefined;
  range: Range;
}

export function metricPath(params: MetricParams, end: number): string {
  const seconds = ranges[params.range];
  const step = stepFor(seconds);
  const parts = [
    `query=${encodeURIComponent(params.query)}`,
    ...(params.namespace ? [`namespace=${encodeURIComponent(params.namespace)}`] : []),
    ...(params.name ? [`name=${encodeURIComponent(params.name)}`] : []),
    `start=${end - seconds}`,
    `end=${end}`,
    `step=${step}`,
  ];
  return `/proxy/v1/metrics/range?${parts.join("&")}`;
}

export function useMetricRanges(paramsList: MetricParams[]): MetricOutcome[] {
  const http = useHttp();
  const results = useQueries({
    queries: paramsList.map((params) => ({
      queryKey: ["metrics", params.query, params.namespace ?? "", params.name ?? "", params.range],
      refetchInterval: 30_000,
      refetchIntervalInBackground: false,
      retry: false,
      queryFn: async (): Promise<MetricOutcome> => {
        const end = Math.floor(Date.now() / 1000);
        try {
          return { unavailable: false, forbidden: false, result: await http.request<MatrixResult>("GET", metricPath(params, end)) };
        } catch (error) {
          if (isHttpError(error) && error.status === 503) {
            return { unavailable: true, forbidden: false, result: undefined };
          }
          if (isHttpError(error) && error.status === 403) {
            return { unavailable: false, forbidden: true, result: undefined };
          }
          throw error;
        }
      },
    })),
  });
  return results.map((result) => result.data ?? { unavailable: false, forbidden: false, result: undefined });
}
