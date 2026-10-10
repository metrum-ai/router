// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent } from "react";
import { Bar, Line } from "react-chartjs-2";
import { Chart as ChartJS } from "chart.js";
import type { ChartData, ChartOptions } from "chart.js";
import type { ReportChart, ReportChartAxis, ReportChartPoint, ReportChartSeries } from "@/lib/reports";
import { buildShortLabelMap, type ShortLabelMap } from "@/lib/shortLabels";
import { formatUtcTimestamp, pickTimeUnit, timeRange, type TimeRange } from "@/lib/timeAxis";
import { compactFmt, usdCompactFmt } from "@/lib/utils";

type Props = {
  charts?: ReportChart[];
  reportId?: string;
};

const palette: Record<string, string> = {
  blue: "#465cda",
  magenta: "#ee0089",
  pink: "#fe005f",
  purple: "#cc28af",
  red: "#ff3132",
  success: "#22c55e",
  text: "#c7c7d1",
  violet: "#9948cb",
  warning: "#f59e0b",
};

export function ReportCharts({ charts, reportId }: Props) {
  const normalized = useMemo(
    () => (charts || []).map(normalizeChart).filter((chart) => chart.series.some((series) => series.points.length > 0)),
    [charts],
  );
  if (normalized.length === 0) {
    return <div className="rounded-lg border border-white/10 p-6 text-sm text-white/62">No chart data for the selected filters.</div>;
  }
  if (reportId === "overview") {
    const sections = overviewChartSections(normalized);
    return (
      <section className="space-y-5" aria-label="Overview charts">
        {sections.map((section) => (
          <div key={section.id} data-chart-section={section.id}>
            <div className="mb-3">
              <h3 className="font-display text-base text-white">{section.title}</h3>
              <p className="text-xs text-white/50">{section.description}</p>
            </div>
            <div className="grid gap-4 xl:grid-cols-2">
              {section.charts.map((chart) => (
                <ChartCard key={chart.id} chart={chart} />
              ))}
            </div>
          </div>
        ))}
      </section>
    );
  }
  return (
    <section className="grid gap-4 xl:grid-cols-2">
      {normalized.map((chart) => (
        <ChartCard key={chart.id} chart={chart} />
      ))}
    </section>
  );
}

type OverviewChartSection = {
  id: string;
  title: string;
  description: string;
  charts: NormalizedChart[];
};

type ChartIdentity = Pick<NormalizedChart, "id" | "title">;

const overviewSectionDefs = [
  {
    id: "health",
    title: "Health",
    description: "Request volume, success, errors, and fallback behavior over the selected window.",
    patterns: [/request/i, /error/i, /fallback/i],
  },
  {
    id: "value",
    title: "Value",
    description: "Actual cost, baseline cost, and savings signals for the selected filters.",
    patterns: [/cost/i, /saving/i, /baseline/i],
  },
  {
    id: "performance",
    title: "Performance",
    description: "Latency, upstream duration, TTFB, and token throughput trends.",
    patterns: [/latency/i, /ttfb/i, /throughput/i, /tok.?s/i, /duration/i],
  },
  {
    id: "operations",
    title: "Operations",
    description: "Cache and security activity that can affect operator triage.",
    patterns: [/cache/i, /security/i, /incident/i, /auth/i],
  },
  {
    id: "topn",
    title: "Top groups",
    description: "Bounded ranked categories for provider, model, caller, and status dimensions.",
    patterns: [/provider/i, /model/i, /token/i, /status/i, /group/i, /user/i, /key/i],
  },
] as const;

export function overviewChartSectionIds(charts: ChartIdentity[]): string[] {
  return overviewChartSections(charts as NormalizedChart[]).map((section) => section.id);
}

function overviewChartSections(charts: NormalizedChart[]): OverviewChartSection[] {
  const buckets = new Map<string, NormalizedChart[]>(overviewSectionDefs.map((section) => [section.id, []]));
  const other: NormalizedChart[] = [];
  for (const chart of charts) {
    const haystack = `${chart.id} ${chart.title}`;
    const section = overviewSectionDefs.find((candidate) => candidate.patterns.some((pattern) => pattern.test(haystack)));
    if (section) {
      buckets.get(section.id)?.push(chart);
    } else {
      other.push(chart);
    }
  }
  if (other.length > 0) {
    buckets.set("other", other);
  }
  const sections: OverviewChartSection[] = overviewSectionDefs
    .map((section) => ({ ...section, charts: buckets.get(section.id) || [] }))
    .filter((section) => section.charts.length > 0);
  if (other.length > 0) {
    sections.push({
      id: "other",
      title: "Additional charts",
      description: "Other chart data returned by the report API.",
      charts: other,
    });
  }
  return sections;
}

function ChartCard({ chart }: { chart: NormalizedChart }) {
  const wrapperRef = useRef<HTMLDivElement | null>(null);
  const data = useMemo(() => chartData(chart), [chart]);
  const options = useMemo(() => chartOptions(chart), [chart]);

  useEffect(() => {
    const hide = () => {
      hideChartTooltip();
      const canvas = wrapperRef.current?.querySelector("canvas");
      const chartInstance = canvas ? ChartJS.getChart(canvas) : undefined;
      chartInstance?.tooltip?.setActiveElements([], { x: 0, y: 0 });
      chartInstance?.update("none");
    };
    window.addEventListener("blur", hide);
    window.addEventListener("resize", hide);
    window.addEventListener("scroll", hide, true);
    window.addEventListener("pointerdown", hide);
    window.addEventListener("visibilitychange", hide);
    window.addEventListener("keydown", hideOnEscape);
    return () => {
      window.removeEventListener("blur", hide);
      window.removeEventListener("resize", hide);
      window.removeEventListener("scroll", hide, true);
      window.removeEventListener("pointerdown", hide);
      window.removeEventListener("visibilitychange", hide);
      window.removeEventListener("keydown", hideOnEscape);
      hideChartTooltip();
    };
  }, []);

  return (
    <div ref={wrapperRef} className="rounded-lg border border-white/10 bg-white/[0.035] p-4" data-chart-id={chart.id}>
      <div className="mb-3">
        <h3 className="font-display text-lg text-white">{chart.title}</h3>
        <p className="text-xs text-white/48">
          {chart.xAxis.label || "Bucket"} by {chart.yAxis.label || chart.yAxis.unit || "value"}
        </p>
      </div>
      <div className="relative h-72" data-chart-canvas>
        {chart.kind === "line" ? (
          <Line data={data as ChartData<"line">} options={options as ChartOptions<"line">} />
        ) : (
          <Bar data={data as ChartData<"bar">} options={options as ChartOptions<"bar">} />
        )}
        <div
          data-chart-hover-layer
          className="absolute inset-0 z-10 bg-transparent pointer-events-auto"
          onMouseEnter={(event) => renderPointerTooltip(event, chart, wrapperRef.current)}
          onMouseMove={(event) => renderPointerTooltip(event, chart, wrapperRef.current)}
          onPointerEnter={(event) => renderPointerTooltip(event, chart, wrapperRef.current)}
          onPointerMove={(event) => renderPointerTooltip(event, chart, wrapperRef.current)}
          onMouseLeave={hideChartTooltip}
          onPointerLeave={hideChartTooltip}
        />
      </div>
      <ChartLegend entries={chart.legendEntries} />
    </div>
  );
}

type NormalizedChart = {
  id: string;
  title: string;
  kind: "bar" | "line";
  xAxis: ReportChartAxis;
  yAxis: ReportChartAxis;
  labels: string[];
  displayLabels: string[];
  legendEntries: ShortLabelMap[];
  hasNumericTimeAxis: boolean;
  timeRange: TimeRange | null;
  series: Array<Required<Pick<ReportChartSeries, "name" | "points">> & { unit?: string; colorKey?: string }>;
};

function normalizeChart(chart: ReportChart): NormalizedChart {
  const xAxis = chart.x_axis || chart.xAxis || {};
  const yAxis = chart.y_axis || chart.yAxis || { unit: chart.unit };
  const rawSeries = Array.isArray(chart.series) ? chart.series : [];
  const series = rawSeries.length > 0 && isBackendSeries(rawSeries[0])
    ? (rawSeries as ReportChartSeries[]).map((item, index) => ({
        name: item.name || `Series ${index + 1}`,
        unit: item.unit,
        colorKey: item.color_key || item.colorKey,
        points: item.points || [],
      }))
    : [
        {
          name: chart.title || "Value",
          unit: chart.unit,
          colorKey: "magenta",
          points: legacyPoints(rawSeries as ReportChartPoint[], chart.points),
        },
      ];
  const kind = chart.kind === "line" || xAxis.type === "time" ? "line" : "bar";
  const hasNumericTimeAxis = kind === "line" && series.every((item) => item.points.every((point) => typeof point.x_unix_ms === "number" && Number.isFinite(point.x_unix_ms)));
  const labels = Array.from(new Set(series.flatMap((item) => item.points.map((point) => point.x || ""))));
  const legendEntries = kind === "bar" ? buildShortLabelMap(labels) : [];
  const fullToShort = new Map(legendEntries.map((entry) => [entry.full, entry.short]));
  const range = hasNumericTimeAxis ? timeRange(series.flatMap((item) => item.points.map((point) => point.x_unix_ms as number))) : null;
  return {
    id: chart.chart_id || chart.chartId || chart.id || chart.title || "chart",
    title: chart.title || "Report chart",
    kind,
    xAxis,
    yAxis,
    labels,
    displayLabels: kind === "bar" ? labels.map((label) => fullToShort.get(label) || label) : labels,
    legendEntries,
    hasNumericTimeAxis,
    timeRange: range,
    series,
  };
}

function isBackendSeries(value: ReportChartPoint | ReportChartSeries): value is ReportChartSeries {
  return "points" in value || "color_key" in value || "colorKey" in value || "name" in value;
}

function legacyPoints(series?: ReportChartPoint[], points?: ReportChartPoint[]): ReportChartPoint[] {
  const input = points?.length ? points : series || [];
  return input.map((point) => ({ x: point.x || point.time || point.label || "", x_unix_ms: point.x_unix_ms, y: point.y ?? point.value ?? 0 }));
}

function chartData(chart: NormalizedChart): ChartData<"bar" | "line"> {
  return {
    labels: chart.hasNumericTimeAxis ? undefined : chart.displayLabels,
    datasets: chart.series.map((series, index) => {
      const color = palette[series.colorKey || ""] || Object.values(palette)[index % Object.keys(palette).length];
      const valuesByLabel = new Map(series.points.map((point) => [point.x || "", point.y ?? point.value ?? 0]));
      const data = chart.hasNumericTimeAxis
        ? series.points.map((point) => ({ x: point.x_unix_ms as number, y: point.y ?? point.value ?? 0 })).sort((left, right) => left.x - right.x)
        : chart.labels.map((label) => valuesByLabel.get(label) || 0);
      return {
        label: series.name,
        data,
        borderColor: color,
        backgroundColor: withAlpha(color, chart.kind === "line" ? 0.18 : 0.7),
        borderWidth: 2,
        tension: 0.28,
        fill: chart.kind === "line",
      };
    }),
  };
}

function chartOptions(chart: NormalizedChart): ChartOptions<"bar" | "line"> {
  return {
    responsive: true,
    maintainAspectRatio: false,
    interaction: { intersect: false, mode: "index" },
    plugins: {
      legend: { labels: { color: "rgba(255,255,255,0.72)", boxWidth: 12, boxHeight: 12 } },
      tooltip: {
        enabled: false,
        callbacks: {
          title: (items) => {
            const item = items[0];
            if (!item) return "";
            if (chart.hasNumericTimeAxis) return formatUtcTimestamp(item.parsed.x ?? Number.NaN);
            return chart.labels[item.dataIndex] || item.label || "";
          },
          label: (item) => `${item.dataset.label}: ${formatChartNumber(chart.hasNumericTimeAxis ? item.parsed.y ?? 0 : Number(item.raw || 0), chart.yAxis.unit)}`,
        },
      },
    },
    scales: {
      x: {
        type: chart.hasNumericTimeAxis ? "time" : "category",
					time: chart.hasNumericTimeAxis
						? {
								unit: pickTimeUnit(chart.timeRange),
								displayFormats: { hour: "hour", day: "day", week: "week", month: "month", year: "year" },
								tooltipFormat: "datetime",
							}
						: undefined,
					ticks: {
						color: "rgba(255,255,255,0.58)",
						maxRotation: chart.kind === "bar" || chart.hasNumericTimeAxis ? 0 : 40,
						minRotation: 0,
						autoSkip: chart.kind === "bar" || chart.hasNumericTimeAxis,
						autoSkipPadding: 12,
						maxTicksLimit: chart.kind === "bar" ? 12 : chart.hasNumericTimeAxis ? 10 : undefined,
					},
        grid: { color: "rgba(255,255,255,0.06)" },
      },
      y: {
        beginAtZero: true,
        ticks: {
          color: "rgba(255,255,255,0.58)",
          callback: (value) => formatChartNumber(Number(value), chart.yAxis.unit),
        },
        grid: { color: "rgba(255,255,255,0.08)" },
      },
    },
  };
}

function hideOnEscape(event: KeyboardEvent) {
  if (event.key === "Escape") hideChartTooltip();
}

function tooltipElement(): HTMLDivElement {
  let tooltip = document.querySelector<HTMLDivElement>("[data-admin-chart-tooltip]");
  if (tooltip) return tooltip;
  tooltip = document.createElement("div");
  tooltip.setAttribute("data-admin-chart-tooltip", "true");
  tooltip.setAttribute("role", "tooltip");
  tooltip.className = "pointer-events-none fixed z-50 max-w-[min(32rem,calc(100vw-2rem))] rounded-md border border-white/20 bg-[#0f1117]/95 px-3 py-2 font-mono text-xs text-white shadow-2xl";
  tooltip.style.display = "none";
  tooltip.style.opacity = "0";
  tooltip.style.transition = "opacity 90ms ease";
  document.body.appendChild(tooltip);
  return tooltip;
}

function hideChartTooltip() {
  document.querySelectorAll<HTMLDivElement>("[data-admin-chart-tooltip]").forEach((tooltip) => {
    tooltip.style.display = "none";
    tooltip.style.opacity = "0";
    tooltip.setAttribute("aria-hidden", "true");
  });
}

function renderPointerTooltip(event: ReactMouseEvent<HTMLDivElement> | ReactPointerEvent<HTMLDivElement>, chart: NormalizedChart, wrapper: HTMLDivElement | null) {
  const pointCount = chart.series[0]?.points.length || 0;
  if (pointCount === 0) {
    hideChartTooltip();
    return;
  }
  const index = pointerChartIndex(event, chart, wrapper, pointCount);
  if (index === null) {
    hideChartTooltip();
    return;
  }
  const title = pointerTooltipTitle(index, chart);
  const rows = chart.series.map((series) => {
    const point = series.points[index];
    return `${series.name}: ${formatChartNumber(Number(point?.y ?? point?.value ?? 0), chart.yAxis.unit)}`;
  });
  showChartTooltip(title, rows, event.clientX + 12, event.clientY + 12);
}

function pointerChartIndex(
  event: ReactMouseEvent<HTMLDivElement> | ReactPointerEvent<HTMLDivElement>,
  chart: NormalizedChart,
  wrapper: HTMLDivElement | null,
  pointCount: number,
): number | null {
  const canvas = wrapper?.querySelector("canvas");
  const chartInstance = canvas ? ChartJS.getChart(canvas) : undefined;
  if (chartInstance && canvas) {
    const canvasRect = canvas.getBoundingClientRect();
    let pixelX = event.clientX - canvasRect.left;
    const area = chartInstance.chartArea;
    if (!area) return null;
    pixelX = Math.min(area.right, Math.max(area.left, pixelX));

    const active = chartInstance.getElementsAtEventForMode(event.nativeEvent, "index", { intersect: false }, false);
    const activeIndex = active[0]?.index;
    if (typeof activeIndex === "number") return clampIndex(activeIndex, pointCount);

    const xScale = chartInstance.scales.x;
    const value = xScale?.getValueForPixel(pixelX);
    if (typeof value === "number" && Number.isFinite(value)) {
      if (chart.hasNumericTimeAxis) {
        let nearestIndex = 0;
        let nearestDistance = Number.POSITIVE_INFINITY;
        chart.series[0]?.points.forEach((point, index) => {
          const ms = typeof point.x_unix_ms === "number" ? point.x_unix_ms : Number.NaN;
          const distance = Math.abs(ms - value);
          if (Number.isFinite(distance) && distance < nearestDistance) {
            nearestDistance = distance;
            nearestIndex = index;
          }
        });
        return clampIndex(nearestIndex, pointCount);
      }
      return clampIndex(Math.round(value), pointCount);
    }
  }

  const rect = event.currentTarget.getBoundingClientRect();
  const ratio = Math.min(0.999999, Math.max(0, (event.clientX - rect.left) / Math.max(rect.width, 1)));
  return clampIndex(Math.floor(ratio * pointCount), pointCount);
}

function clampIndex(index: number, pointCount: number): number {
  return Math.min(pointCount - 1, Math.max(0, index));
}

function pointerTooltipTitle(index: number, chart: NormalizedChart): string {
  if (chart.hasNumericTimeAxis) {
    const ms = chart.series[0]?.points[index]?.x_unix_ms;
    return formatUtcTimestamp(typeof ms === "number" ? ms : Number.NaN);
  }
  return chart.labels[index] || "";
}

function showChartTooltip(title: string, rows: string[], x: number, y: number) {
  const element = tooltipElement();
  element.replaceChildren();
  const titleEl = document.createElement("div");
  titleEl.className = "mb-1 wrap-break-word text-white/92";
  titleEl.textContent = title;
  element.appendChild(titleEl);
  for (const row of rows) {
    const rowEl = document.createElement("div");
    rowEl.className = "text-white/78";
    rowEl.textContent = row;
    element.appendChild(rowEl);
  }
  element.style.left = "0px";
  element.style.top = "0px";
  element.style.display = "block";
  element.style.opacity = "0";
  const bounds = element.getBoundingClientRect();
  const margin = 16;
  const left = x + bounds.width + margin > window.innerWidth ? x - bounds.width - 24 : x;
  const top = y + bounds.height + margin > window.innerHeight ? y - bounds.height - 24 : y;
  const maxLeft = Math.max(margin, window.innerWidth - bounds.width - margin);
  const maxTop = Math.max(margin, window.innerHeight - bounds.height - margin);
  element.style.left = `${Math.min(maxLeft, Math.max(margin, left))}px`;
  element.style.top = `${Math.min(maxTop, Math.max(margin, top))}px`;
  element.style.opacity = "1";
  element.removeAttribute("aria-hidden");
}

function ChartLegend({ entries }: { entries: ShortLabelMap[] }) {
  const [open, setOpen] = useState(entries.length <= 8);
  if (entries.length === 0) return null;
  return (
    <div className="mt-3 text-[0.7rem] text-white/62" data-chart-bucket-legend>
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        className="mb-2 inline-flex items-center gap-1 rounded border border-white/10 px-2 py-1 text-white/70 hover:bg-white/5 focus:outline-hidden focus:ring-2 focus:ring-white/30"
        aria-expanded={open}
      >
        {open ? "Hide" : "Show"} bucket legend ({entries.length})
      </button>
      {open && (
        <ul className="grid grid-cols-1 gap-x-4 gap-y-1 sm:grid-cols-2 xl:grid-cols-3">
          {entries.map((entry) => (
            <li key={entry.full} className="flex min-w-0 font-mono" title={entry.full}>
              <span className="mr-2 inline-block min-w-[4ch] shrink-0 text-white/48">{entry.short}</span>
              <span className="truncate align-bottom">{entry.full}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function formatChartNumber(value: number, unit?: string): string {
  if (unit === "usd") return usdCompactFmt.format(value);
  if (unit === "percent") return `${compactFmt.format(value)}%`;
  if (unit === "ms") return `${compactFmt.format(value)} ms`;
  if (unit === "tokens") return `${compactFmt.format(value)} tok`;
  return compactFmt.format(value);
}

function withAlpha(hex: string, alpha: number): string {
  const red = parseInt(hex.slice(1, 3), 16);
  const green = parseInt(hex.slice(3, 5), 16);
  const blue = parseInt(hex.slice(5, 7), 16);
  return `rgba(${red}, ${green}, ${blue}, ${alpha})`;
}
