import { describe, expect, it } from "vitest";
import type { MatrixResult } from "../api/metrics";
import { toAlignedData } from "./toAlignedData";

const matrix = (...series: { metric: Record<string, string>; values: [number, string][] }[]): MatrixResult => ({ status: "success", data: { resultType: "matrix", result: series } });

describe("toAlignedData", () => {
  it("TestAlignsSeriesOnOneTimeAxis", () => {
    const { data, labels } = toAlignedData(
      matrix(
        { metric: { mode: "user" }, values: [[60, "1"], [120, "2"], [180, "3"]] },
        { metric: { mode: "system" }, values: [[120, "5"], [180, "6"], [240, "7"]] },
      ),
      60,
      240,
      60,
    );
    expect(data[0]).toEqual([60, 120, 180, 240]);
    expect(data[1]).toEqual([1, 2, 3, null]);
    expect(data[2]).toEqual([null, 5, 6, 7]);
    expect(labels).toEqual(["user", "system"]);
    expect(data[1]).toHaveLength(data[0].length);
  });

  it("TestDropsNonFiniteValues", () => {
    const { data } = toAlignedData(matrix({ metric: {}, values: [[60, "NaN"], [120, "+Inf"], [180, "-Inf"], [240, "4"]] }), 60, 240, 60);
    expect(data[1]).toEqual([null, null, null, 4]);
  });

  it("TestEmptyResult gives one empty axis", () => {
    expect(toAlignedData(matrix(), 0, 100, 10)).toEqual({ data: [[]], labels: [] });
  });

  it("TestSeriesLabelsComeFromMetricLabels", () => {
    const { labels } = toAlignedData(
      matrix(
        { metric: { device: "vda" }, values: [[60, "1"]] },
        { metric: { interface: "eth0" }, values: [[60, "1"]] },
        { metric: {}, values: [[60, "1"]] },
      ),
      60,
      60,
      60,
      "vm.cpu",
    );
    expect(labels).toEqual(["vda", "eth0", "vm.cpu"]);
  });
});
