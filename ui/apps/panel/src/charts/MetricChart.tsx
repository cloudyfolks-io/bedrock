import { useEffect, useMemo, useRef, useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { ranges, stepFor, useMetricRanges, type MetricOutcome, type Range } from "../api/metrics";
import { formatUnit, type Unit } from "../format";
import { useLocale } from "../LocaleProvider";
import { RangePicker } from "./RangePicker";
import { toAlignedData, type AlignedData } from "./toAlignedData";
import type { ChartHandle } from "./uplotAdapter";

interface Props {
  title: string;
  queries: string[];
  unit: Unit;
  namespace?: string | undefined;
  name?: string | undefined;
}

const chartHeight = 220;

function cssColor(name: string, fallback: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
}

function mergeOutcomes(queries: string[], outcomes: MetricOutcome[], start: number, end: number, step: number): { data: AlignedData; labels: string[] } {
  const parts = outcomes.map((outcome, index) => (outcome.result ? toAlignedData(outcome.result, start, end, step, queries[index] ?? "value") : { data: [[]] as AlignedData, labels: [] as string[] }));
  const times = [...new Set(parts.flatMap((part) => part.data[0]))].sort((a, b) => a - b);
  const columns = parts.flatMap((part) => {
    const index = new Map(part.data[0].map((time, position) => [time, position] as const));
    return part.data.slice(1).map((column) => times.map((time) => (index.has(time) ? (column[index.get(time) ?? 0] ?? null) : null)));
  });
  return { data: [times, ...columns] as AlignedData, labels: parts.flatMap((part) => part.labels) };
}

export function MetricChart({ title, queries, unit, namespace, name }: Props) {
  const { t } = useLingui();
  const { locale } = useLocale();
  const [range, setRange] = useState<Range>("1h");
  const [themeVersion, setThemeVersion] = useState(0);
  const container = useRef<HTMLDivElement>(null);
  const handle = useRef<ChartHandle | undefined>(undefined);
  const outcomes = useMetricRanges(queries.map((query) => ({ query, namespace, name, range })));
  const unavailable = outcomes.some((outcome) => outcome.unavailable);
  const forbidden = outcomes.length > 0 && outcomes.every((outcome) => outcome.forbidden);
  const end = Math.floor(Date.now() / 1000);
  const step = stepFor(ranges[range]);
  const merged = useMemo(() => mergeOutcomes(queries, outcomes, end - ranges[range], end, step), [queries, outcomes, range, step, end]);
  const hasData = merged.labels.length > 0;
  const dataRef = useRef(merged.data);
  dataRef.current = merged.data;
  const seriesKey = merged.labels.join("|");

  useEffect(() => {
    const observer = new MutationObserver(() => setThemeVersion((value) => value + 1));
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    if (unavailable || forbidden || !hasData || !container.current) {
      return undefined;
    }
    let cancelled = false;
    const target = container.current;
    void import("./uplotAdapter").then((adapter) => {
      if (cancelled) {
        return;
      }
      const series = merged.labels.map((label, index) => ({ label, stroke: cssColor(`--br-color-chart-${(index % 4) + 1}`, "currentColor") }));
      handle.current = adapter.createChart(target, target.clientWidth || 600, chartHeight, series, dataRef.current, cssColor("--br-color-text", "currentColor"));
    });
    const resize = new ResizeObserver(() => handle.current?.setSize(target.clientWidth || 600, chartHeight));
    resize.observe(target);
    return () => {
      cancelled = true;
      resize.disconnect();
      handle.current?.destroy();
      handle.current = undefined;
    };
  }, [unavailable, forbidden, hasData, seriesKey, themeVersion]);

  useEffect(() => {
    handle.current?.setData(merged.data);
  }, [merged.data]);

  if (forbidden) {
    return null;
  }
  const values = (merged.data.slice(1) as (number | null)[][]).flatMap((column) => column.filter((value): value is number => value !== null));
  const summary =
    values.length === 0
      ? ""
      : t({
          id: "metrics.summary",
          message: `${title}: minimum ${formatUnit(Math.min(...values), unit, locale)}, maximum ${formatUnit(Math.max(...values), unit, locale)}, last ${formatUnit(values[values.length - 1] ?? 0, unit, locale)}`,
        });

  return (
    <section className="br-chart">
      <div className="br-toolbar">
        <h3>{title}</h3>
        <RangePicker value={range} onChange={setRange} />
      </div>
      {unavailable ? (
        <p role="status">{t({ id: "metrics.unavailable", message: "Metrics are not available" })}</p>
      ) : (
        <>
          <div ref={container} dir="ltr" role="img" aria-label={title} className="br-chart-canvas" />
          <p className="br-visually-hidden">{summary}</p>
        </>
      )}
    </section>
  );
}
