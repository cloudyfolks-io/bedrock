import { screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { metricPath, ranges, stepFor, useMetricRanges, type MetricOutcome } from "./metrics";
import { renderPanel } from "../test/renderPanel";

function Probe({ outcomes }: { outcomes: (value: MetricOutcome[]) => void }) {
  const result = useMetricRanges([{ query: "vm.cpu", namespace: "web-ns", name: "web-1", range: "1h" }]);
  outcomes(result);
  return <p>{result[0]?.unavailable ? "unavailable" : result[0]?.forbidden ? "forbidden" : result[0]?.result ? "ok" : "loading"}</p>;
}

describe("metrics", () => {
  it("TestStepFor", () => {
    expect(stepFor(ranges["1h"])).toBe(15);
    expect(stepFor(ranges["6h"])).toBe(15);
    expect(stepFor(ranges["24h"])).toBe(60);
    expect(stepFor(ranges["7d"])).toBe(600);
    for (const seconds of Object.values(ranges)) {
      expect(seconds / stepFor(seconds)).toBeLessThanOrEqual(1500);
    }
  });

  it("TestRequestParameters", async () => {
    expect(metricPath({ query: "vm.cpu", namespace: "web-ns", name: "web-1", range: "24h" }, 1_000_000)).toBe(
      "/proxy/v1/metrics/range?query=vm.cpu&namespace=web-ns&name=web-1&start=913600&end=1000000&step=60",
    );
    expect(metricPath({ query: "ceph.used", range: "1h" }, 5000)).toBe("/proxy/v1/metrics/range?query=ceph.used&start=1400&end=5000&step=15");
    const { api } = renderPanel(<Probe outcomes={() => {}} />, {
      api: { "GET /proxy/v1/metrics/range": { body: { status: "success", data: { resultType: "matrix", result: [] } } } },
    });
    expect(await screen.findByText("ok")).toBeTruthy();
    const call = api.calls.find((entry) => entry.path === "/proxy/v1/metrics/range");
    expect(call?.query.get("query")).toBe("vm.cpu");
    expect(call?.query.get("namespace")).toBe("web-ns");
    expect(call?.query.get("step")).toBe("15");
    expect(Number.isInteger(Number(call?.query.get("start")))).toBe(true);
  });

  it("TestMetricsUnavailable gives unavailable and does not retry", async () => {
    const { api } = renderPanel(<Probe outcomes={() => {}} />, { api: { "GET /proxy/v1/metrics/range": { status: 503, body: { error: "metrics_unavailable" } } } });
    expect(await screen.findByText("unavailable")).toBeTruthy();
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(api.calls.filter((entry) => entry.path === "/proxy/v1/metrics/range")).toHaveLength(1);
  });

  it("hides the chart on a 403", async () => {
    renderPanel(<Probe outcomes={() => {}} />, { api: { "GET /proxy/v1/metrics/range": { status: 403, body: { error: "forbidden" } } } });
    await waitFor(() => expect(screen.getByText("forbidden")).toBeTruthy());
  });
});
