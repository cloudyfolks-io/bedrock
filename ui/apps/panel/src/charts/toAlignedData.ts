import type { MatrixResult } from "../api/metrics";

export type AlignedData = [number[], ...(number | null)[][]];

const labelKeys = ["mode", "device", "interface"];

function numeric(text: string): number | null {
  const value = Number(text);
  return Number.isFinite(value) ? value : null;
}

export function toAlignedData(result: MatrixResult, start: number, end: number, step: number, fallback = "value"): { data: AlignedData; labels: string[] } {
  const series = result.data.result;
  const snap = (timestamp: number) => Math.round(timestamp / step) * step;
  const times = [...new Set(series.flatMap((entry) => entry.values.map(([timestamp]) => snap(timestamp))))].filter((timestamp) => timestamp >= snap(start) && timestamp <= snap(end)).sort((a, b) => a - b);
  if (times.length === 0) {
    return { data: [[]], labels: [] };
  }
  const columns = series.map((entry) => {
    const byTime = new Map(entry.values.map(([timestamp, value]) => [snap(timestamp), numeric(value)] as const));
    return times.map((timestamp) => byTime.get(timestamp) ?? null);
  });
  const labels = series.map((entry) => labelKeys.map((key) => entry.metric[key]).find((value) => value !== undefined && value !== "") ?? fallback);
  return { data: [times, ...columns], labels };
}
