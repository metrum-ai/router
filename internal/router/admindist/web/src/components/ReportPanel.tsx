// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import type { Dispatch, SetStateAction } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { DataTable } from "@/components/DataTable";
import { MetricGrid, type MetricGridConfig } from "@/components/MetricGrid";
import { ReportCharts } from "@/components/ReportCharts";
import { TabFilterPanel } from "@/components/TabFilterPanel";
import { Button } from "@/components/ui/button";
import type { ReportFilters, ReportPageAction, ReportResponse, TabSpec } from "@/lib/reports";
import { columnsForTab, resolveSortKey, rowsForTab, tabById } from "@/lib/reports";

type Props = {
  tab: TabSpec;
  report?: ReportResponse;
  filters: ReportFilters;
  pageIndex: number | null;
  canGoBack: boolean;
  loading: boolean;
  error?: string;
  onFiltersChange: Dispatch<SetStateAction<ReportFilters>>;
  onPageChange: (action: ReportPageAction, cursor?: string) => void;
  onRefresh: () => void;
};

export function ReportPanel({ tab, report, filters, pageIndex, canGoBack, loading, error, onFiltersChange, onPageChange, onRefresh }: Props) {
  const rows = report ? rowsForTab(tab, report) : [];
  const columns = columnsForTab(tab, rows);
  const supportedSortKeys = supportedSortKeysForTab(tab);
  const sortKey = resolveSortKey(report?.pagination?.sort || filters.sort, rows, columns);
  const sortDir = (report?.pagination?.direction || filters.direction) === "asc" ? "asc" : "desc";
  const metricGridConfig = metricGridConfigForTab(tab, report);
  return (
    <main className="space-y-4">
      <div>
        <p className="font-mono text-xs uppercase text-metrum-red">{tab.endpoint}</p>
        <h2 className="font-display text-2xl text-white">{tab.label}</h2>
        <p className="mt-1 max-w-4xl text-sm text-white/70">{tab.metadata.shortDescription}</p>
        <p className="mt-1 text-sm text-white/50">
          {report?.period?.from && report?.period?.to ? `${report.period.from} to ${report.period.to}` : report?.generatedUtc ? `Generated ${report.generatedUtc}` : "Current report"}
        </p>
      </div>
      <details className="rounded-lg border border-white/10 bg-white/[0.035] p-4 text-sm text-white/72">
        <summary className="cursor-pointer select-none font-mono text-xs uppercase text-white/70 focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-metrum-blue">
          How to use this report
        </summary>
        <div className="mt-3 grid gap-4 lg:grid-cols-[1.25fr_1fr]">
          <div className="space-y-3">
            <p>{tab.metadata.purpose}</p>
            <p className="text-white/58">{tab.metadata.dataSemantics}</p>
            <p className="text-white/58">{tab.metadata.caveats}</p>
          </div>
          <div className="space-y-3">
            <HelpList title="Common filters" items={tab.metadata.commonFilters} />
            <HelpList
              title="Key columns"
              items={tab.metadata.keyColumns.slice(0, 4).map((column) => `${column.key}: ${column.description}`)}
            />
            <div>
              <p className="font-mono text-[0.68rem] uppercase text-white/50">Related reports</p>
              <div className="mt-2 flex flex-wrap gap-2">
                {tab.metadata.relatedReports
                  .map((id) => tabById(id))
                  .filter((related): related is TabSpec => Boolean(related))
                  .map((related) => (
                    <a
                      key={related.id}
                      href={relatedReportHref(related.id)}
                      className="rounded-md border border-white/10 px-2 py-1 text-xs text-white/70 hover:border-metrum-blue/50 hover:text-white focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-metrum-blue"
                    >
                      {related.label}
                    </a>
                  ))}
              </div>
            </div>
          </div>
        </div>
      </details>
      {error ? (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-metrum-red/40 bg-metrum-red/10 p-4 text-sm text-white">
          <span>{error}</span>
          {filters.cursor ? (
            <Button type="button" variant="outline" onClick={() => onPageChange("first")}>
              Reset to first page
            </Button>
          ) : null}
        </div>
      ) : null}
      {loading ? <div className="rounded-lg border border-white/10 p-6 text-sm text-white/62">Loading report data...</div> : null}
      <TabFilterPanel tab={tab} columns={columns} supportedSortKeys={supportedSortKeys} filters={filters} onFiltersChange={onFiltersChange} />
      <MetricGrid summary={report?.summary} config={metricGridConfig} />
      {tab.id === "data-migrations" && rows.length > 0 ? (
        <details className="rounded-lg border border-white/10 bg-white/[0.035] p-4" data-migration-detail-panel>
          <summary className="cursor-pointer font-mono text-xs uppercase text-white/70">Migration detail and audit</summary>
          <dl className="mt-3 grid gap-2 text-sm text-white/70 md:grid-cols-2">
            {[["Plan", rows[0].name], ["Postcondition", rows[0].postcondition], ["Effective state", rows[0].state], ["Data-job state", rows[0].dataJobState || "not bound"], ["Validation", rows[0].validationState], ["Audit", rows[0].startedAt], ["Progress", `${rows[0].checkpoints || 0} checkpoints; ${rows[0].rowsScanned || 0} scanned; ${rows[0].rowsUpdated || 0} updated; ${rows[0].rowsSkipped || 0} skipped; ${rows[0].rowsFailed || 0} failed`], ["Safe error", rows[0].errorMessage || rows[0].errorClass || "none"]].map(([label, value]) => <div key={String(label)}><dt className="font-mono text-xs uppercase text-white/45">{String(label)}</dt><dd>{String(value || "not recorded")}</dd></div>)}
          </dl>
        </details>
      ) : null}
      {tab.id === "savings" && report?.warnings?.length ? (
        <Card className="border-metrum-red/30 bg-metrum-red/10">
          <CardHeader>
            <CardTitle>Warnings</CardTitle>
          </CardHeader>
          <CardContent>
            <ul className="list-disc space-y-1 pl-5 text-sm text-white/75">
              {report.warnings.map((warning) => (
                <li key={warning}>{warning}</li>
              ))}
            </ul>
          </CardContent>
        </Card>
      ) : null}
      <ReportCharts charts={report?.charts} reportId={tab.id} />
      <DataTable
        rows={rows}
        columns={columns}
        initialSortKey={sortKey}
        initialSortDir={sortDir}
        limit={filters.limit}
        onLimitChange={(limit) => onFiltersChange((current) => ({ ...current, limit }))}
        pagination={report?.pagination}
        supportedSortKeys={supportedSortKeys}
        pageIndex={pageIndex}
        canGoBack={canGoBack}
        loading={loading}
        onPageChange={onPageChange}
        onSortChange={(sort, direction) => onFiltersChange((current) => ({ ...current, sort, direction }))}
        onRefresh={onRefresh}
      />
    </main>
  );
}

export function metricGridConfigForTab(tab: TabSpec, report?: ReportResponse): MetricGridConfig | undefined {
  if (tab.id === "data-migrations") {
    return {
      priority: ["state", "pending", "inProgress", "failed", "verified", "missingDataJobs", "jobs"],
		labels: { state: "Effective state", inProgress: "In progress (including running schema work)", missingDataJobs: "Missing data jobs", jobs: "Data jobs" },
      hideZeroKeys: ["pending", "inProgress", "failed", "verified", "missingDataJobs"],
      maxItems: 7,
    };
  }
  if (!tab.savings) return undefined;
  const hasActualCost = typeof report?.summary?.actualCostUsd === "number" || typeof report?.summary?.actual_cost_usd === "number";
  return {
    priority: [
      "savingsUsd",
      "savings_usd",
      "savingsPct",
      "savings_pct",
      "actualCostUsd",
      "actual_cost_usd",
      "totalCostUsd",
      "baselineCostUsd",
      "baseline_cost_usd",
      "requests",
      "totalTokens",
      "total_tokens",
      "tokens",
      "avgCostUsd",
      "errors",
      "fallbacks",
    ],
    labels: {
      savingsUsd: "Total savings",
      savings_usd: "Total savings",
      savingsPct: "Savings rate",
      savings_pct: "Savings rate",
      actualCostUsd: "Actual cost",
      actual_cost_usd: "Actual cost",
      totalCostUsd: "Actual cost",
      baselineCostUsd: "Baseline cost",
      baseline_cost_usd: "Baseline cost",
      avgCostUsd: "Avg cost/request",
    },
    hiddenKeys: hasActualCost ? ["totalCostUsd"] : undefined,
    hideZeroKeys: ["errors", "fallbacks"],
    maxItems: 9,
  };
}

function relatedReportHref(tabId: string) {
  const params = new URLSearchParams(window.location.search);
  params.set("tab", tabId);
  params.delete("cursor");
  return `?${params.toString()}`;
}

function HelpList({ title, items }: { title: string; items: string[] }) {
  if (!items.length) return null;
  return (
    <div>
      <p className="font-mono text-[0.68rem] uppercase text-white/50">{title}</p>
      <ul className="mt-1 space-y-1 text-white/62">
        {items.map((item) => (
          <li key={item}>{item}</li>
        ))}
      </ul>
    </div>
  );
}

export function supportedSortKeysForTab(tab: TabSpec): ReadonlySet<string> | undefined {
  if (tab.security) return new Set(["timeUtc", "status", "outcome", "surface", "reason"]);
  if (tab.requests) return new Set(["timeUtc", "costUsd", "totalCostUsd", "latencyMs", "status", "requestId"]);
  if (!tab.filters?.includes("sort")) return undefined;
  if (tab.savings && tab.id.startsWith("savings-by-")) {
    return new Set(["savingsUsd", "savingsPct", "baselineCostUsd", "actualCostUsd", "totalCostUsd", "requests", "totalTokens", "avgCostUsd"]);
  }
  return new Set([
    "key",
    "requests",
    "costUsd",
    "totalCostUsd",
    "tokens",
    "totalTokens",
    "latencyMs",
    "avgLatencyMs",
    "errors",
    "fallbacks",
    "savingsUsd",
    "inputImageCount",
  ]);
}
