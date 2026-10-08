import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderPanel } from "../test/renderPanel";
import { MetricChart } from "./MetricChart";

const mocks = vi.hoisted(() => ({
  handle: { setData: vi.fn(), setSize: vi.fn(), destroy: vi.fn() },
  createChart: vi.fn(),
  resizeCallbacks: [] as (() => void)[],
}));

vi.mock("./uplotAdapter", () => ({ createChart: mocks.createChart }));

const now = Math.floor(Date.now() / 1000);
const matrix = { status: "success", data: { resultType: "matrix", result: [{ metric: {}, values: [[now - 60, "1"], [now, "3"]] }] } };

class FakeResizeObserver {
  constructor(callback: () => void) {
    mocks.resizeCallbacks.push(callback);
  }

  observe() {}
  disconnect() {}
}

function render(api: Record<string, { status?: number; body?: unknown }> = { "GET /proxy/v1/metrics/range": { body: matrix } }, locale: "en" | "fa" = "en") {
  return renderPanel(<MetricChart title="CPU use" queries={["vm.cpu"]} unit="cores" namespace="web-ns" name="web-1" />, { api, locale });
}

describe("MetricChart", () => {
  beforeEach(() => {
    mocks.createChart.mockReset().mockReturnValue(mocks.handle);
    for (const fn of Object.values(mocks.handle)) {
      fn.mockReset();
    }
    mocks.resizeCallbacks.length = 0;
    vi.stubGlobal("ResizeObserver", FakeResizeObserver);
    document.documentElement.style.setProperty("--br-color-chart-1", "#111111");
    document.documentElement.style.setProperty("--br-color-text", "#222222");
    document.documentElement.removeAttribute("data-theme");
  });

  it("TestChartCreatesUPlotWithThemeColors and recreates it when the theme changes", async () => {
    render();
    await waitFor(() => expect(mocks.createChart).toHaveBeenCalledTimes(1));
    const first = mocks.createChart.mock.calls[0] as unknown[];
    expect((first[3] as { stroke: string }[])[0]?.stroke).toBe("#111111");
    document.documentElement.style.setProperty("--br-color-chart-1", "#abcdef");
    act(() => {
      document.documentElement.dataset.theme = "dark";
    });
    await waitFor(() => expect(mocks.createChart).toHaveBeenCalledTimes(2));
    expect(mocks.handle.destroy).toHaveBeenCalled();
    expect((mocks.createChart.mock.calls[1]?.[3] as { stroke: string }[])[0]?.stroke).toBe("#abcdef");
  });

  it("TestChartIsLtrInRtl while the title is localized", async () => {
    render(undefined, "fa");
    await waitFor(() => expect(mocks.createChart).toHaveBeenCalled());
    expect(screen.getByRole("img", { name: "CPU use" }).getAttribute("dir")).toBe("ltr");
    expect(document.documentElement.dir).toBe("rtl");
  });

  it("TestChartDestroysOnUnmount", async () => {
    const view = render();
    await waitFor(() => expect(mocks.createChart).toHaveBeenCalledTimes(1));
    view.unmount();
    expect(mocks.handle.destroy).toHaveBeenCalledTimes(1);
  });

  it("TestChartResizesWithItsContainer", async () => {
    render();
    await waitFor(() => expect(mocks.createChart).toHaveBeenCalledTimes(1));
    act(() => mocks.resizeCallbacks.forEach((callback) => callback()));
    expect(mocks.handle.setSize).toHaveBeenCalled();
  });

  it("TestUnavailableShowsAMessage and creates no chart", async () => {
    render({ "GET /proxy/v1/metrics/range": { status: 503, body: { error: "metrics_unavailable" } } });
    expect(await screen.findByText("Metrics are not available")).toBeTruthy();
    expect(screen.getByRole("status")).toBeTruthy();
    expect(mocks.createChart).not.toHaveBeenCalled();
  });

  it("hides the chart when metrics are forbidden", async () => {
    const { container } = render({ "GET /proxy/v1/metrics/range": { status: 403, body: { error: "forbidden" } } });
    await waitFor(() => expect(container.querySelector(".br-chart")).toBeNull());
  });

  it("TestChartHasATextAlternative with minimum, maximum and last value", async () => {
    render();
    expect(await screen.findByText(/minimum 1, maximum 3, last 3/)).toBeTruthy();
    expect(screen.getByRole("img", { name: "CPU use" })).toBeTruthy();
  });

  it("TestRangePickerUpdatesTheQuery", async () => {
    const user = userEvent.setup();
    const { api } = render();
    await waitFor(() => expect(mocks.createChart).toHaveBeenCalled());
    await user.click(screen.getByRole("combobox", { name: /Time range/ }));
    await user.click(await screen.findByRole("option", { name: "Last 24 hours" }));
    await waitFor(() => expect(api.calls.filter((call) => call.path === "/proxy/v1/metrics/range" && call.query.get("step") === "60")).toHaveLength(1));
  });
});
