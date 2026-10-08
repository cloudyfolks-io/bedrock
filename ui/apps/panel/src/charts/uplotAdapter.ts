import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import type { AlignedData } from "./toAlignedData";

export interface ChartSeries {
  label: string;
  stroke: string;
}

export interface ChartHandle {
  setData: (data: AlignedData) => void;
  setSize: (width: number, height: number) => void;
  destroy: () => void;
}

export function createChart(target: HTMLElement, width: number, height: number, series: ChartSeries[], data: AlignedData, textColor: string): ChartHandle {
  const chart = new uPlot(
    {
      width,
      height,
      series: [{}, ...series.map((entry) => ({ label: entry.label, stroke: entry.stroke, width: 2 }))],
      axes: [{ stroke: textColor }, { stroke: textColor }],
      legend: { show: true },
    },
    data,
    target,
  );
  return {
    setData: (next) => chart.setData(next),
    setSize: (nextWidth, nextHeight) => chart.setSize({ width: nextWidth, height: nextHeight }),
    destroy: () => chart.destroy(),
  };
}
