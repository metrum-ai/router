// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import { Card, CardContent } from "@/components/ui/card";
import { formatValue, titleize } from "@/lib/utils";
import type { ReportSummary } from "@/lib/reports";

const defaultPriority = ["requests", "errors", "totalTokens", "tokens", "totalCostUsd", "costUsd", "avgLatencyMs", "fallbacks"];

export type MetricGridConfig = {
  priority?: string[];
  labels?: Record<string, string>;
  hiddenKeys?: string[];
  hideZeroKeys?: string[];
  maxItems?: number;
};

export type MetricEntry = {
  key: string;
  label: string;
  value: string | number | boolean;
};

export function buildMetricEntries(summary?: ReportSummary, config: MetricGridConfig = {}): MetricEntry[] {
  if (!summary) return [];
  const priority = config.priority ?? defaultPriority;
  const hiddenKeys = new Set(config.hiddenKeys ?? []);
  const hideZeroKeys = new Set(config.hideZeroKeys ?? []);
  const entries = Object.entries(summary)
    .flatMap(([key, value]) => {
      if (hiddenKeys.has(key)) return [];
      if (hideZeroKeys.has(key) && value === 0) return [];
      if (typeof value !== "number" && typeof value !== "string" && typeof value !== "boolean") return [];
      return [{ key, value }];
    })
    .sort((a, b) => {
      const ai = priority.indexOf(a.key);
      const bi = priority.indexOf(b.key);
      if (ai === -1 && bi === -1) return a.key.localeCompare(b.key);
      if (ai === -1) return 1;
      if (bi === -1) return -1;
      return ai - bi;
    })
    .slice(0, config.maxItems ?? 8)
    .map(({ key, value }) => ({ key, value, label: config.labels?.[key] ?? titleize(key) }));
  return entries;
}

export function MetricGrid({ summary, config }: { summary?: ReportSummary; config?: MetricGridConfig }) {
  const entries = buildMetricEntries(summary, config);
  if (entries.length === 0) return null;
  return (
    <section className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
      {typeof summary?.estimatedTokenShare === "number" && (
        <Card className="panel-gradient"><CardContent className="p-4">
          <div className="font-display text-2xl text-white">{summary.estimatedTokenShare.toFixed(1)}%</div>
          <div className="mt-1 font-mono text-xs uppercase text-white/58">Estimated usage</div>
          <div className="text-xs text-white/58">Share of tokens estimated, not upstream-reported</div>
        </CardContent></Card>
      )}
      {entries.map(({ key, label, value }) => (
        <Card key={key} className="panel-gradient">
          <CardContent className="p-4">
            <div className="font-display text-2xl text-white">{formatValue(value, key)}</div>
            <div className="mt-1 font-mono text-xs uppercase text-white/58">{label}</div>
          </CardContent>
        </Card>
      ))}
    </section>
  );
}
