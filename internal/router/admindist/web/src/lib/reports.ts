// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import type { TabFilterName } from "@/lib/filters";
export { filterFields, globalFilterFields, tabFilterFields, validateFilterModel, type GlobalFilterName, type TabFilterName } from "@/lib/filters";

export type ReportFilters = Record<string, string>;

export type ReportPeriod = {
  from?: string;
  to?: string;
};

export type ReportSummary = Record<string, number | string | boolean | null | undefined>;

export type ReportPagination = {
  limit?: number;
  returned?: number;
  total_count?: number | null;
  has_more?: boolean;
  next_cursor?: string;
  prev_cursor?: string;
  sort?: string;
  direction?: "asc" | "desc" | string;
  mode?: "cursor" | "top_n" | string;
  offset?: number;
  note?: string;
};

export type ReportPageAction = "first" | "previous" | "next" | "last";

export type ReportChartPoint = {
  x?: string;
  x_unix_ms?: number;
  y?: number;
  label?: string;
  time?: string;
  value?: number;
  values?: Record<string, number>;
};

export type ReportChartAxis = {
  label?: string;
  type?: string;
  unit?: string;
};

export type ReportChartSeries = {
  name?: string;
  unit?: string;
  color_key?: string;
  colorKey?: string;
  points?: ReportChartPoint[];
};

export type ReportChart = {
  id?: string;
  chart_id?: string;
  chartId?: string;
  title?: string;
  unit?: string;
  kind?: string;
  x_axis?: ReportChartAxis;
  xAxis?: ReportChartAxis;
  y_axis?: ReportChartAxis;
  yAxis?: ReportChartAxis;
  series?: ReportChartSeries[] | ReportChartPoint[];
  points?: ReportChartPoint[];
};

export type ReportResponse = {
  period?: ReportPeriod;
  report?: string;
  summary?: ReportSummary;
  rows?: ReportRow[];
  requests?: ReportRow[];
  charts?: ReportChart[];
  series?: ReportChart[];
  cache?: Record<string, unknown>;
  byToken?: ReportRow[];
  byGroup?: ReportRow[];
  byProvider?: ReportRow[];
  byStatus?: ReportRow[];
  generatedUtc?: string;
  baseline?: Record<string, unknown>;
  baselines?: Array<Record<string, unknown>>;
  byTime?: ReportRow[];
  warnings?: string[];
  pagination?: ReportPagination;
};

export type ReportRow = Record<string, unknown>;

export type VersionResponse = {
  version?: string;
  commit?: string;
  build_date?: string;
  go_version?: string;
  goos?: string;
  goarch?: string;
  license_compile_mode?: string;
};

export type ReportColumn = {
  key: string;
  label: string;
  unit?: string;
  description?: string;
};

export type ReportKeyColumnHelp = {
  key: string;
  description: string;
};

export type ReportMetadata = {
  shortDescription: string;
  purpose: string;
  dataSemantics: string;
  commonFilters: string[];
  keyColumns: ReportKeyColumnHelp[];
  caveats: string;
  relatedReports: string[];
  docsPath: string;
  emptyState: string;
};

export type TabSpec = {
  id: string;
  label: string;
  endpoint: string;
  metadata: ReportMetadata;
  columns?: ReportColumn[];
  savings?: boolean;
  overview?: boolean;
  requests?: boolean;
  security?: boolean;
  filters?: ReadonlyArray<TabFilterName>;
};

const identityColumns: ReportColumn[] = [
  { key: "key", label: "Key" },
  { key: "secondaryKey", label: "Secondary key" },
];

const trafficColumns: ReportColumn[] = [
  { key: "requests", label: "Requests" },
  { key: "attempts", label: "Attempts" },
  { key: "streams", label: "Streams" },
  { key: "errors", label: "Errors" },
  { key: "errorRatePct", label: "Error rate", unit: "%" },
  { key: "fallbacks", label: "Fallbacks" },
  { key: "fallbackRatePct", label: "Fallback rate", unit: "%" },
];

const shapingColumns: ReportColumn[] = [
  { key: "requests", label: "Events" },
  { key: "rejections", label: "Rejections" },
  { key: "queued", label: "Queued" },
  { key: "skippedTargets", label: "Skipped targets" },
  { key: "cooldownsStarted", label: "Cooldowns" },
  { key: "avgRetryAfterMs", label: "Avg retry-after", unit: "ms" },
  { key: "maxRetryAfterMs", label: "Max retry-after", unit: "ms" },
  { key: "avgQueueWaitMs", label: "Avg queue wait", unit: "ms" },
  { key: "p50QueueWaitMs", label: "P50 queue wait", unit: "ms" },
  { key: "p95QueueWaitMs", label: "P95 queue wait", unit: "ms" },
  { key: "maxQueueWaitMs", label: "Max queue wait", unit: "ms" },
  { key: "estimatedInputTokens", label: "Estimated input" },
  { key: "reservedOutputTokens", label: "Reserved output" },
  { key: "totalReservedTokens", label: "Total reserved" },
  { key: "upstream429Attempts", label: "Upstream 429" },
  { key: "upstreamQuotaAttempts", label: "Upstream quota" },
  { key: "routeAroundSuccesses", label: "Route-around OK" },
];

const trafficTuningColumns: ReportColumn[] = [
  ...identityColumns,
  { key: "recommendation", label: "Recommendation" },
  { key: "severity", label: "Severity" },
  { key: "requests", label: "Requests" },
  { key: "successes", label: "Successes" },
  { key: "errors", label: "Errors" },
  { key: "router429", label: "Router 429" },
  { key: "trafficShaped", label: "Shaped" },
  { key: "rejections", label: "Rejected" },
  { key: "queued", label: "Queued" },
  { key: "p95QueueWaitMs", label: "P95 queue wait", unit: "ms" },
  { key: "maxRetryAfterMs", label: "Max retry-after", unit: "ms" },
  { key: "upstream400", label: "Upstream 400" },
  { key: "upstream429Attempts", label: "Upstream 429" },
  { key: "upstream5xx", label: "Upstream 5xx" },
  { key: "upstreamTimeouts", label: "Timeouts" },
  { key: "clientCanceled", label: "Canceled" },
  { key: "fallbackRatePct", label: "Fallback rate", unit: "%" },
  { key: "observedValues", label: "Evidence" },
  { key: "threshold", label: "Threshold" },
  { key: "configFields", label: "Config fields" },
  { key: "explanation", label: "Explanation" },
];

const tokenColumns: ReportColumn[] = [
  { key: "inputTokens", label: "Input tokens" },
  { key: "outputTokens", label: "Output tokens" },
  { key: "inputImageTokens", label: "Image tokens" },
  { key: "totalTokens", label: "Total tokens" },
  { key: "input_tokens", label: "Input tokens" },
  { key: "output_tokens", label: "Output tokens" },
  { key: "total_tokens", label: "Total tokens" },
];

const costColumns: ReportColumn[] = [
  { key: "inputCostUsd", label: "Input cost", unit: "USD" },
  { key: "imageCostUsd", label: "Image cost", unit: "USD" },
  { key: "outputCostUsd", label: "Output cost", unit: "USD" },
  { key: "totalCostUsd", label: "Total cost", unit: "USD" },
  { key: "actual_cost_usd", label: "Actual cost", unit: "USD" },
  { key: "baseline_cost_usd", label: "Baseline cost", unit: "USD" },
  { key: "savings_usd", label: "Savings", unit: "USD" },
  { key: "savings_pct", label: "Savings rate", unit: "%" },
  { key: "avgCostUsd", label: "Avg cost/request", unit: "USD" },
  { key: "upstreamReportedCostUsd", label: "Upstream billed", unit: "USD" },
  { key: "baselineCostUsd", label: "Baseline cost", unit: "USD" },
  { key: "savingsUsd", label: "Savings", unit: "USD" },
  { key: "savingsPct", label: "Savings rate", unit: "%" },
];

const latencyColumns: ReportColumn[] = [
  { key: "avgLatencyMs", label: "Avg latency", unit: "ms" },
  { key: "maxLatencyMs", label: "Max latency", unit: "ms" },
  { key: "avgTtfbMs", label: "Avg TTFB", unit: "ms" },
  { key: "maxTtfbMs", label: "Max TTFB", unit: "ms" },
  { key: "avgUpstreamMs", label: "Avg upstream", unit: "ms" },
  { key: "maxUpstreamMs", label: "Max upstream", unit: "ms" },
  { key: "avgDownstreamMs", label: "Avg downstream", unit: "ms" },
  { key: "maxDownstreamMs", label: "Max downstream", unit: "ms" },
];

const diagnosticColumns: ReportColumn[] = [
  ...identityColumns,
  { key: "status", label: "Status" },
  { key: "errorClass", label: "Error class" },
  { key: "provider", label: "Provider" },
  { key: "model", label: "Model" },
  { key: "dialect", label: "Dialect" },
  { key: "requestShapeFingerprint", label: "Shape FP" },
  { key: "toolSchemaFingerprint", label: "Tool FP" },
  { key: "requests", label: "Affected requests" },
  { key: "errors", label: "Errors" },
  { key: "attempts", label: "Attempts" },
  { key: "retryableAttempts", label: "Retryable" },
  { key: "timeoutAttempts", label: "Timeouts" },
  { key: "fallbacks", label: "Fallbacks" },
  { key: "fallbackSucceeded", label: "Fallback OK" },
  { key: "fallbackFailed", label: "Fallback failed" },
  { key: "terminalErrors", label: "Terminal errors" },
  { key: "upstreamErrorDetails", label: "Error details" },
  { key: "fieldsStrippedCount", label: "Fields stripped" },
  { key: "fieldsRewrittenCount", label: "Fields rewritten" },
  { key: "unsupportedFieldsCount", label: "Unsupported fields" },
  { key: "translationWarningCount", label: "Translation warnings" },
  { key: "affectedUsers", label: "Users" },
  { key: "affectedClients", label: "Clients" },
  ...latencyColumns,
];

const throughputColumns: ReportColumn[] = [
  { key: "avgUpstreamTokensPerSec", label: "Upstream tokens/sec", unit: "tok/s" },
  { key: "avgUpstreamOutputTokensPerSec", label: "Upstream output tok/s", unit: "tok/s" },
  { key: "avgUpstreamTotalTokensPerSec", label: "Upstream total tok/s", unit: "tok/s" },
  { key: "avgDownstreamTokensPerSec", label: "Downstream tokens/sec", unit: "tok/s" },
  { key: "avgDownstreamWriteOutputTokensPerSec", label: "Downstream output tok/s", unit: "tok/s" },
  { key: "avgDownstreamWriteTotalTokensPerSec", label: "Downstream total tok/s", unit: "tok/s" },
];

const cacheColumns: ReportColumn[] = [
  { key: "cacheHits", label: "Cache hits" },
  { key: "cacheMisses", label: "Cache misses" },
  { key: "cacheBypass", label: "Cache bypass" },
  { key: "cacheHitRatePct", label: "Cache hit rate", unit: "%" },
  { key: "latestCacheItems", label: "Latest cache items" },
  { key: "latestCacheBytes", label: "Latest cache bytes" },
  { key: "latestCacheMaxBytes", label: "Cache max bytes" },
  { key: "latestCacheOccupancyPct", label: "Cache occupancy", unit: "%" },
];

const defaultScalarColumns: ReportColumn[] = [
  ...identityColumns,
  ...trafficColumns,
  ...tokenColumns,
  ...costColumns,
  ...latencyColumns,
  ...throughputColumns,
];

const savingsColumns: ReportColumn[] = [
  { key: "key", label: "Bucket" },
  { key: "requests", label: "Requests" },
  { key: "input_tokens", label: "Input tokens" },
  { key: "output_tokens", label: "Output tokens" },
  { key: "total_tokens", label: "Total tokens" },
  { key: "actual_cost_usd", label: "Actual cost", unit: "USD" },
  { key: "baseline_cost_usd", label: "Baseline cost", unit: "USD" },
  { key: "savings_usd", label: "Savings", unit: "USD" },
  { key: "savings_pct", label: "Savings rate", unit: "%" },
];

function savingsBreakdownColumns(primaryLabel: string, secondaryLabel?: string): ReportColumn[] {
  const dimensionColumns: ReportColumn[] = [{ key: "key", label: primaryLabel }];
  if (secondaryLabel) {
    dimensionColumns.push({ key: "secondaryKey", label: secondaryLabel });
  }
  return [
    ...dimensionColumns,
    { key: "requests", label: "Requests" },
    { key: "inputTokens", label: "Input tokens" },
    { key: "outputTokens", label: "Output tokens" },
    { key: "totalTokens", label: "Total tokens" },
    { key: "actualCostUsd", label: "Actual cost", unit: "USD" },
    { key: "baselineCostUsd", label: "Baseline cost", unit: "USD" },
    { key: "savingsUsd", label: "Savings", unit: "USD" },
    { key: "savingsPct", label: "Savings rate", unit: "%" },
    { key: "avgCostUsd", label: "Avg cost/request", unit: "USD" },
  ];
}

const requestColumns: ReportColumn[] = [
  { key: "timeUtc", label: "Time" },
  { key: "requestId", label: "Request ID" },
  { key: "callerId", label: "Caller" },
  { key: "callerUser", label: "User" },
  { key: "project", label: "Project" },
  { key: "environment", label: "Environment" },
  { key: "callerIp", label: "Caller IP" },
  { key: "tokenId", label: "Token ID" },
  { key: "client", label: "Client" },
  { key: "requestedModel", label: "Requested model" },
  { key: "modelGroup", label: "Model group" },
  { key: "provider", label: "Provider" },
  { key: "model", label: "Target model" },
  { key: "dialect", label: "Dialect" },
  { key: "status", label: "Status" },
  { key: "cache", label: "Cache" },
  { key: "attempts", label: "Attempts" },
  { key: "fallback", label: "Fallback" },
  { key: "inputTokens", label: "Input tokens" },
  { key: "outputTokens", label: "Output tokens" },
  { key: "totalTokens", label: "Total tokens" },
  { key: "costUsd", label: "Cost", unit: "USD" },
  { key: "totalCostUsd", label: "Total cost", unit: "USD" },
  { key: "latencyMs", label: "Latency", unit: "ms" },
  { key: "ttfbMs", label: "TTFB", unit: "ms" },
  { key: "upstreamMs", label: "Upstream", unit: "ms" },
  { key: "downstreamMs", label: "Downstream", unit: "ms" },
  { key: "error", label: "Error" },
];

const securityColumns: ReportColumn[] = [
  { key: "timeUtc", label: "Time" },
  { key: "requestId", label: "Request ID" },
  { key: "eventType", label: "Event type" },
  { key: "surface", label: "Surface" },
  { key: "method", label: "Method" },
  { key: "path", label: "Path" },
  { key: "status", label: "Status" },
  { key: "outcome", label: "Outcome" },
  { key: "reason", label: "Reason" },
  { key: "authSubject", label: "Auth subject" },
  { key: "authSource", label: "Auth source" },
  { key: "callerId", label: "Caller" },
  { key: "callerUser", label: "User" },
  { key: "project", label: "Project" },
  { key: "adminSubject", label: "Admin subject" },
  { key: "client", label: "Client" },
  { key: "userAgentFamily", label: "User agent" },
  { key: "ipAddress", label: "IP address" },
  { key: "ipSource", label: "IP source" },
  { key: "modelGroup", label: "Model group" },
  { key: "requestedModel", label: "Requested model" },
  { key: "resolvedGroup", label: "Resolved group" },
  { key: "inputTokens", label: "Input tokens" },
  { key: "outputTokens", label: "Output tokens" },
  { key: "totalTokens", label: "Total tokens" },
];

const retentionColumns: ReportColumn[] = [
  { key: "dataClass", label: "Data class" },
  { key: "tableName", label: "Table" },
  { key: "rollupType", label: "Rollup type" },
  { key: "status", label: "Status" },
  { key: "retentionDays", label: "Retention days" },
  { key: "candidateRows", label: "Candidate rows" },
  { key: "eligibleRows", label: "Eligible rows" },
  { key: "heldRows", label: "Held rows" },
  { key: "blockedRows", label: "Blocked rows" },
  { key: "deletedRows", label: "Deleted rows" },
  { key: "sourceRequestCount", label: "Source requests" },
  { key: "dailyRows", label: "Daily rows" },
  { key: "rollupRows", label: "Rollup rows" },
  { key: "windowStart", label: "Window start" },
  { key: "windowEnd", label: "Window end" },
  { key: "completedAt", label: "Completed" },
  { key: "message", label: "Message" },
];

const migrationColumns: ReportColumn[] = [
  { key: "scope", label: "Scope" }, { key: "migrationId", label: "Migration ID" }, { key: "name", label: "Migration" }, { key: "release", label: "Release" }, { key: "state", label: "Effective state" }, { key: "schemaVersion", label: "Schema version" }, { key: "dataVersion", label: "Data version" }, { key: "maintenanceMode", label: "Maintenance" }, { key: "executionMode", label: "Execution" }, { key: "lockClass", label: "Lock class" }, { key: "timeoutClass", label: "Timeout class" }, { key: "rollbackClass", label: "Rollback" }, { key: "dataJobKey", label: "Data job" }, { key: "dataJobState", label: "Data-job state" }, { key: "durationMs", label: "Duration", unit: "ms" }, { key: "errorClass", label: "Safe error class" }, { key: "errorMessage", label: "Safe error message" }, { key: "validationState", label: "Validation" }, { key: "postcondition", label: "Postcondition" }, { key: "rowsScanned", label: "Rows scanned" }, { key: "rowsUpdated", label: "Rows updated" }, { key: "rowsSkipped", label: "Rows skipped" }, { key: "rowsFailed", label: "Rows failed" }, { key: "checkpoints", label: "Checkpoints" }, { key: "startedAt", label: "Started" }, { key: "completedAt", label: "Completed" },
];

const catalogColumns: ReportColumn[] = [
  { key: "source", label: "Source" },
  { key: "provider", label: "Provider" },
  { key: "modelRef", label: "Model ref" },
  { key: "model", label: "Model" },
  { key: "dialect", label: "Dialect" },
  { key: "activeGroups", label: "Active groups" },
  { key: "activeTargetCount", label: "Active targets" },
  { key: "validationStatus", label: "Validation" },
  { key: "validationWorkload", label: "Workload" },
  { key: "validationAgeBucket", label: "Validation age" },
  { key: "qualityScore", label: "Quality score" },
  { key: "passRate", label: "Pass rate", unit: "%" },
  { key: "contextTokens", label: "Context tokens" },
  { key: "inputModalities", label: "Input modalities" },
  { key: "outputModalities", label: "Output modalities" },
  { key: "toolSupport", label: "Tool support" },
  { key: "activeEligibilitySkin", label: "Active skin" },
  { key: "effectiveToolSupport", label: "Effective tools" },
  { key: "inactiveToolSupport", label: "Inactive tools" },
  { key: "effectiveStructuredOutputs", label: "Effective structured" },
  { key: "effectiveReasoning", label: "Effective reasoning" },
  { key: "effectiveImageInput", label: "Effective image" },
  { key: "eligibilityWarning", label: "Eligibility warning" },
  { key: "forceStoreFalse", label: "Force store false" },
  { key: "outputTokenField", label: "Output token field" },
  { key: "inputPricePerMillionUsd", label: "Input price/M", unit: "USD" },
  { key: "outputPricePerMillionUsd", label: "Output price/M", unit: "USD" },
  { key: "pricingSource", label: "Pricing source" },
  { key: "pricingUpdatedAt", label: "Pricing updated" },
  { key: "pricingMissing", label: "Pricing missing" },
];

const statusCacheFilters = ["status", "cache"] as const satisfies ReadonlyArray<TabFilterName>;
const savingsFilters = ["baseline", "sort", "direction"] as const satisfies ReadonlyArray<TabFilterName>;
const statusCacheSortFilters = ["status", "cache", "sort", "direction"] as const satisfies ReadonlyArray<TabFilterName>;
const shapingFilters = ["traffic_shape_bucket", "traffic_shape_scope", "sort", "direction"] as const satisfies ReadonlyArray<TabFilterName>;
const sortDirectionFilters = ["sort", "direction"] as const satisfies ReadonlyArray<TabFilterName>;

const baseFilters = [
  "since",
  "caller_id",
  "caller_user",
  "project",
  "environment",
  "requested_model",
  "resolved_group",
  "provider",
  "model",
  "dialect",
  "client",
  "error_class",
  "request_shape_fingerprint",
  "tool_schema_fingerprint",
];
const statusFilters = [...baseFilters, "status", "cache"];
const shapingHelpFilters = [...baseFilters, "traffic_shape_bucket", "traffic_shape_scope"];
const savingsHelpFilters = [...baseFilters, "baseline", "sort", "direction"];
const securityHelpFilters = ["since", "caller_id", "caller_user", "project", "client", "status", "sort", "direction"];

function docsPath(id: string) {
  return `/docs/operations/admin-browser-reports#${id}`;
}

function meta(id: string, metadata: Omit<ReportMetadata, "docsPath">): ReportMetadata {
  return { ...metadata, docsPath: docsPath(id) };
}

export const reportMetadataById = {
  "data-migrations": meta("data-migrations", { shortDescription: "Shows the checked-in migration contract and safe ledger state. This page is read-only.", purpose: "Use it before an upgrade or recovery to confirm compatibility, pending work, maintenance class, validation, audit timing, and rollback contract.", dataSemantics: "Checked-in metadata joined to scalar ledger state; SQL, DSNs, credentials, content, and write actions are excluded.", commonFilters: ["scope", "release", "state", "type", "date"], keyColumns: [{ key: "state", description: "Safe ledger state." }, { key: "rollbackClass", description: "Release rollback requirement." }], caveats: "Apply, retry, and recovery remain deployment-job CLI operations with approved evidence; this dashboard never performs them.", relatedReports: ["retention-status", "overview"], emptyState: "No migration definitions are registered for this deployment." }),
  overview: meta("overview", {
    shortDescription: "Shows overall usage, spend, latency, cache, fallback, and provider trends.",
    purpose: "Start here to confirm whether the selected time range is healthy before drilling into a specific dimension.",
    dataSemantics: "Summary aggregates plus top-N supporting rows for the selected filters.",
    commonFilters: baseFilters,
    keyColumns: [
      { key: "requests", description: "Total routed requests in the selected period." },
      { key: "totalCostUsd", description: "Stored request-time calculated cost for the selected period." },
      { key: "avgLatencyMs", description: "Average caller-observed request latency." },
    ],
    caveats: "Overview is a triage entry point; use detail tabs for cursor-paged request review.",
    relatedReports: ["requests", "provider-model-mix", "latency-throughput"],
    emptyState: "Broaden the time range or remove filters if the overview has no rows.",
  }),
  groups: meta("groups", {
    shortDescription: "Breaks traffic down by resolved model group.",
    purpose: "Use it to compare deployment-defined model-group adoption, cost, latency, and error rate.",
    dataSemantics: "Top-N aggregate by model group for the selected filters.",
    commonFilters: statusFilters,
    keyColumns: [
      { key: "key", description: "Resolved model group bucket." },
      { key: "requests", description: "Requests routed through the group." },
      { key: "totalCostUsd", description: "Stored cost attributed to the group." },
    ],
    caveats: "Model group names are deployment-defined; they are not product-required constants.",
    relatedReports: ["provider-model-mix", "requests", "contract-buckets"],
    emptyState: "No model-group usage matched the current filters.",
  }),
  providers: meta("providers", {
    shortDescription: "Breaks traffic down by upstream provider.",
    purpose: "Use it to see which providers are carrying traffic and where cost, latency, or errors concentrate.",
    dataSemantics: "Top-N aggregate by provider for the selected filters.",
    commonFilters: statusFilters,
    keyColumns: [
      { key: "key", description: "Provider bucket." },
      { key: "attempts", description: "Upstream attempts sent to the provider." },
      { key: "errors", description: "Requests or attempts with error status in this bucket." },
    ],
    caveats: "Use Provider/model for model-level detail and Requests for individual rows.",
    relatedReports: ["provider-model-mix", "errors-fallbacks", "latency-throughput"],
    emptyState: "No provider usage matched the current filters.",
  }),
  tokens: meta("tokens", {
    shortDescription: "Breaks usage down by public router key label.",
    purpose: "Use it for key-level chargeback, rotation review, and noisy-key triage without exposing raw tokens.",
    dataSemantics: "Top-N aggregate by public token ID for the selected filters.",
    commonFilters: statusFilters,
    keyColumns: [
      { key: "key", description: "Public token ID or safe key label." },
      { key: "requests", description: "Requests authenticated with the key." },
      { key: "totalTokens", description: "Input plus output tokens attributed to the key." },
    ],
    caveats: "Raw bearer tokens and token hashes are never shown.",
    relatedReports: ["usage-by-key", "security-events", "requests"],
    emptyState: "No key usage matched the current filters.",
  }),
  savings: meta("savings", {
    shortDescription: "Compares actual stored cost with a selected baseline price.",
    purpose: "Use it to quantify savings for the selected window before slicing by owner, key, group, project, or provider/model.",
    dataSemantics: "Summary plus top-N savings rows by time and group.",
    commonFilters: savingsHelpFilters,
    keyColumns: [
      { key: "actual_cost_usd", description: "Stored request-time actual cost." },
      { key: "baseline_cost_usd", description: "Hypothetical cost from the selected source-dated baseline." },
      { key: "savings_usd", description: "Baseline cost minus actual cost." },
    ],
    caveats: "Actual spend is stored at request time; only the hypothetical baseline is calculated at report time.",
    relatedReports: ["savings-by-project", "expensive-requests", "provider-model-mix"],
    emptyState: "No savings rows matched the selected baseline and filters.",
  }),
  "savings-by-user": meta("savings-by-user", {
    shortDescription: "Ranks savings by caller owner user.",
    purpose: "Use it to explain which users or teams benefit most from routing versus the selected baseline.",
    dataSemantics: "Top-N aggregate by caller user.",
    commonFilters: savingsHelpFilters,
    keyColumns: [
      { key: "key", description: "Caller owner user bucket." },
      { key: "requests", description: "Requests attributed to the user." },
      { key: "totalCostUsd", description: "Stored actual cost for that user." },
      { key: "baselineCostUsd", description: "Hypothetical baseline cost for that user." },
      { key: "savingsUsd", description: "Baseline cost minus actual cost." },
      { key: "savingsPct", description: "Savings as a percentage of baseline cost." },
    ],
    caveats: "Rows depend on configured caller ownership metadata.",
    relatedReports: ["usage-by-caller", "savings-by-project", "requests"],
    emptyState: "No user savings rows matched the current filters.",
  }),
  "savings-by-key": meta("savings-by-key", {
    shortDescription: "Ranks savings by public router key label.",
    purpose: "Use it for key-level savings review, rotation cleanup, and chargeback questions.",
    dataSemantics: "Top-N aggregate by public token ID.",
    commonFilters: savingsHelpFilters,
    keyColumns: [
      { key: "key", description: "Public token ID or safe key label." },
      { key: "requests", description: "Requests attributed to the key." },
      { key: "totalCostUsd", description: "Stored actual cost for the key." },
      { key: "baselineCostUsd", description: "Hypothetical baseline cost for the key." },
      { key: "savingsUsd", description: "Baseline minus actual cost for the key." },
      { key: "savingsPct", description: "Savings as a percentage of baseline cost." },
    ],
    caveats: "Raw router tokens and token hashes are never exposed.",
    relatedReports: ["tokens", "usage-by-key", "requests"],
    emptyState: "No key savings rows matched the current filters.",
  }),
  "savings-by-group": meta("savings-by-group", {
    shortDescription: "Ranks savings by resolved model group.",
    purpose: "Use it to compare cost efficiency across deployment-defined groups.",
    dataSemantics: "Top-N aggregate by model group.",
    commonFilters: savingsHelpFilters,
    keyColumns: [
      { key: "key", description: "Resolved model group bucket." },
      { key: "requests", description: "Requests served by the group." },
      { key: "totalCostUsd", description: "Stored actual cost for that group." },
      { key: "baselineCostUsd", description: "Hypothetical baseline cost for the group." },
      { key: "savingsUsd", description: "Baseline minus actual cost for the group." },
      { key: "savingsPct", description: "Savings rate versus baseline." },
    ],
    caveats: "Group savings should be reviewed with quality or contract validation evidence before promotion decisions.",
    relatedReports: ["groups", "contract-buckets", "validation"],
    emptyState: "No model-group savings rows matched the current filters.",
  }),
  "savings-by-project": meta("savings-by-project", {
    shortDescription: "Ranks savings by caller project.",
    purpose: "Use it for project chargeback and business-owner reporting.",
    dataSemantics: "Top-N aggregate by project.",
    commonFilters: savingsHelpFilters,
    keyColumns: [
      { key: "key", description: "Project bucket." },
      { key: "secondaryKey", description: "Environment bucket when present." },
      { key: "requests", description: "Requests attributed to the project." },
      { key: "totalCostUsd", description: "Stored actual cost for the project." },
      { key: "baselineCostUsd", description: "Hypothetical baseline cost for the project." },
      { key: "savingsUsd", description: "Estimated project savings." },
      { key: "savingsPct", description: "Savings as a percentage of baseline cost." },
    ],
    caveats: "Project attribution depends on configured caller/project membership metadata.",
    relatedReports: ["project-chargeback", "savings-by-user", "expensive-requests"],
    emptyState: "No project savings rows matched the current filters.",
  }),
  "savings-by-provider-model": meta("savings-by-provider-model", {
    shortDescription: "Ranks savings by serving provider and model.",
    purpose: "Use it to identify which upstream mixes generate or erode savings against a baseline.",
    dataSemantics: "Top-N aggregate by provider/model.",
    commonFilters: savingsHelpFilters,
    keyColumns: [
      { key: "key", description: "Provider/model bucket." },
      { key: "secondaryKey", description: "API dialect bucket when present." },
      { key: "requests", description: "Requests served by the provider/model bucket." },
      { key: "totalCostUsd", description: "Stored actual cost for that upstream bucket." },
      { key: "baselineCostUsd", description: "Hypothetical baseline cost for that upstream bucket." },
      { key: "savingsUsd", description: "Estimated savings versus baseline." },
      { key: "savingsPct", description: "Savings as a percentage of baseline cost." },
    ],
    caveats: "Savings alone does not prove workload quality; pair with validation and request outcomes.",
    relatedReports: ["provider-model-mix", "target-validation", "requests"],
    emptyState: "No provider/model savings rows matched the current filters.",
  }),
  "model-groups-by-user": meta("model-groups-by-user", {
    shortDescription: "Shows which model groups each user is consuming.",
    purpose: "Use it to review access patterns, migration progress, or user-specific group adoption.",
    dataSemantics: "Top-N aggregate by user and model group.",
    commonFilters: statusFilters,
    keyColumns: [
      { key: "key", description: "Primary user or user/group bucket." },
      { key: "secondaryKey", description: "Secondary grouping such as model group." },
      { key: "requests", description: "Requests in that user/group bucket." },
    ],
    caveats: "Rows depend on safe caller owner metadata.",
    relatedReports: ["groups", "usage-by-caller", "requests"],
    emptyState: "No user/model-group rows matched the current filters.",
  }),
  "usage-by-key": meta("usage-by-key", {
    shortDescription: "Shows usage, cost, and performance by public key label.",
    purpose: "Use it for key-level operations, key rotation review, and quota sizing.",
    dataSemantics: "Top-N aggregate by key.",
    commonFilters: statusFilters,
    keyColumns: [
      { key: "key", description: "Public token ID or safe key label." },
      { key: "errorRatePct", description: "Error percentage for requests using the key." },
      { key: "avgLatencyMs", description: "Average latency for key traffic." },
    ],
    caveats: "Use Security for failed or forbidden access events.",
    relatedReports: ["tokens", "quotas-budgets", "security-events"],
    emptyState: "No key usage rows matched the current filters.",
  }),
  "usage-by-caller": meta("usage-by-caller", {
    shortDescription: "Shows usage by caller identity or owner.",
    purpose: "Use it to locate noisy callers, support user incidents, or compare team traffic.",
    dataSemantics: "Top-N aggregate by caller.",
    commonFilters: statusFilters,
    keyColumns: [
      { key: "key", description: "Caller or owner bucket." },
      { key: "requests", description: "Requests attributed to the caller." },
      { key: "totalCostUsd", description: "Stored request-time cost for the caller." },
    ],
    caveats: "For individual failed calls, open Requests with the same caller filter.",
    relatedReports: ["requests", "usage-by-key", "client-breakdown"],
    emptyState: "No caller usage rows matched the current filters.",
  }),
  "requested-models": meta("requested-models", {
    shortDescription: "Shows requested model group names before target selection.",
    purpose: "Use it to review caller demand, deprecated group usage, and routing policy inputs.",
    dataSemantics: "Top-N aggregate by requested model.",
    commonFilters: statusFilters,
    keyColumns: [
      { key: "key", description: "Requested model or group value." },
      { key: "requests", description: "Requests asking for that value." },
      { key: "fallbacks", description: "Requests that needed alternate upstream attempts." },
    ],
    caveats: "Requested model may differ from the resolved group or selected upstream target.",
    relatedReports: ["groups", "routing-decisions", "requests"],
    emptyState: "No requested-model rows matched the current filters.",
  }),
  "provider-model-mix": meta("provider-model-mix", {
    shortDescription: "Shows actual serving provider/model/dialect mix.",
    purpose: "Use it to compare upstream share, cost, latency, throughput, errors, and fallbacks.",
    dataSemantics: "Top-N aggregate by selected provider/model/dialect.",
    commonFilters: statusFilters,
    keyColumns: [
      { key: "key", description: "Provider/model/dialect bucket." },
      { key: "attempts", description: "Upstream attempts in the bucket." },
      { key: "avgUpstreamTokensPerSec", description: "Average upstream throughput where available." },
    ],
    caveats: "It is not cursor-paged detail; use Requests for row-by-row review.",
    relatedReports: ["latency-throughput", "errors-fallbacks", "requests"],
    emptyState: "No provider/model rows matched the current filters.",
  }),
  "latency-throughput": meta("latency-throughput", {
    shortDescription: "Shows downstream latency, upstream timing, TTFB, and throughput buckets.",
    purpose: "Use it during slow-UX or slow-upstream investigations.",
    dataSemantics: "Top-N aggregate by selected performance bucket.",
    commonFilters: statusFilters,
    keyColumns: [
      { key: "avgLatencyMs", description: "Average caller-observed end-to-end latency." },
      { key: "avgTtfbMs", description: "Average time to first byte for streaming or first response." },
      { key: "avgDownstreamTokensPerSec", description: "Average downstream write throughput." },
    ],
    caveats: "Latency can reflect queueing, upstream time, downstream client behavior, or output size.",
    relatedReports: ["provider-model-mix", "requests", "traffic-shaping-overview"],
    emptyState: "No latency rows matched the current filters.",
  }),
  "errors-fallbacks": meta("errors-fallbacks", {
    shortDescription: "Shows error and fallback volume by status/error bucket and provider/model.",
    purpose: "Use it during incident triage to find failing upstreams, caller-visible errors, and fallback behavior.",
    dataSemantics: "Top-N aggregate by selected filters unless pagination metadata says otherwise.",
    commonFilters: statusFilters,
    keyColumns: [
      { key: "errors", description: "Requests with HTTP status >= 400." },
      { key: "fallbacks", description: "Requests that used one or more alternate upstream attempts." },
      { key: "errorRatePct", description: "Error percentage within the bucket." },
    ],
    caveats: "Fallback success may hide upstream instability from callers; inspect attempts on request detail.",
    relatedReports: ["requests", "provider-model-mix", "latency-throughput"],
    emptyState: "No error or fallback rows matched the current filters.",
  }),
  "upstream-failures": meta("upstream-failures", {
    shortDescription: "Groups upstream failures by provider, model, dialect, status, and safe provider error bucket.",
    purpose: "Use it during provider incidents to identify which upstream target and safe error bucket are driving failures.",
    dataSemantics: "Top-N aggregate by upstream provider/model/dialect, HTTP status, router error, sanitized provider code/param, and categorized provider message.",
    commonFilters: [...baseFilters, "status", "sort", "direction"],
    keyColumns: [
      { key: "key", description: "Provider/model/dialect/status/error bucket." },
      { key: "secondaryKey", description: "Affected user/client bucket." },
      { key: "errors", description: "Caller-visible failed requests in the bucket." },
    ],
    caveats: "Provider messages are categorized; raw provider prose and response bodies are not shown.",
    relatedReports: ["request-shape-failures", "fallback-health", "requests"],
    emptyState: "No upstream failure rows matched the current filters.",
  }),
  "request-shape-failures": meta("request-shape-failures", {
    shortDescription: "Compares failed and successful requests by safe request-shape and translation buckets.",
    purpose: "Use it to find request shapes that a provider/model/dialect rejects while similar traffic succeeds elsewhere.",
    dataSemantics: "Top-N aggregate by safe request/translation shape bucket plus non-reversible request and tool-schema fingerprints.",
    commonFilters: [...baseFilters, "status", "sort", "direction"],
    keyColumns: [
      { key: "key", description: "Safe request/translation shape bucket and fingerprints." },
      { key: "secondaryKey", description: "Provider/model bucket." },
      { key: "errorRatePct", description: "Failure percentage for the shape bucket." },
    ],
    caveats: "Fingerprints are non-reversible and cannot reconstruct prompts or tool schemas.",
    relatedReports: ["upstream-failures", "requests", "provider-model-mix"],
    emptyState: "No request-shape rows matched the current filters.",
  }),
  "fallback-health": meta("fallback-health", {
    shortDescription: "Shows whether fallback attempts recover failures or still end in caller-visible errors.",
    purpose: "Use it to validate that fallback policy is protecting callers when an upstream target fails.",
    dataSemantics: "Top-N aggregate by model group, fallback outcome, attempt count, terminal status, and provider/model.",
    commonFilters: [...baseFilters, "status", "sort", "direction"],
    keyColumns: [
      { key: "key", description: "Model group and fallback outcome bucket." },
      { key: "fallbacks", description: "Requests where one or more fallback attempts were used." },
      { key: "errorRatePct", description: "Caller-visible error percentage after fallback handling." },
    ],
    caveats: "Use request drilldown to inspect the exact attempt chain for a single request.",
    relatedReports: ["upstream-failures", "errors-fallbacks", "requests"],
    emptyState: "No fallback health rows matched the current filters.",
  }),
  "user-client-impact": meta("user-client-impact", {
    shortDescription: "Ranks affected users and clients by errors, latency, and model group.",
    purpose: "Use it to answer which users, projects, and clients are impacted by an incident.",
    dataSemantics: "Top-N aggregate by caller user/client and model group.",
    commonFilters: [...baseFilters, "status", "sort", "direction"],
    keyColumns: [
      { key: "key", description: "Caller user and client bucket." },
      { key: "secondaryKey", description: "Resolved model group bucket." },
      { key: "avgLatencyMs", description: "Average caller-observed latency for impacted traffic." },
    ],
    caveats: "User and client values are deployment-provided safe metadata.",
    relatedReports: ["upstream-failures", "fallback-health", "requests"],
    emptyState: "No user/client impact rows matched the current filters.",
  }),
  "cache-report": meta("cache-report", {
    shortDescription: "Shows cache hits, misses, bypasses, occupancy, and hit rate.",
    purpose: "Use it to tune cache behavior and explain spend or latency changes from cache use.",
    dataSemantics: "Top-N aggregate by cache-related bucket.",
    commonFilters: statusFilters,
    keyColumns: [
      { key: "cacheHits", description: "Requests served from router cache without an upstream call." },
      { key: "cacheMisses", description: "Cacheable requests that did not find a stored response." },
      { key: "cacheBypass", description: "Requests intentionally bypassed by request shape or policy." },
    ],
    caveats: "Image-bearing requests and no-cache requests bypass caching.",
    relatedReports: ["savings", "latency-throughput", "requests"],
    emptyState: "No cache rows matched the current filters.",
  }),
  "quotas-budgets": meta("quotas-budgets", {
    shortDescription: "Shows quota, budget, key-state, and admission-related usage buckets.",
    purpose: "Use it for rate-limit, TPM/RPM, daily/monthly budget, and key lifecycle triage.",
    dataSemantics: "Top-N aggregate by quota or budget bucket.",
    commonFilters: ["since", "caller_id", "caller_user", "project", "client", "status"],
    keyColumns: [
      { key: "requests", description: "Requests in the quota or budget bucket." },
      { key: "errors", description: "Rejected or failed requests in the bucket." },
      { key: "totalTokens", description: "Actual or reserved token usage represented by the bucket." },
    ],
    caveats: "Use Traffic shaping for burst smoothing and Admission for eligibility reasons.",
    relatedReports: ["traffic-shaping-overview", "admission-reasons", "requests"],
    emptyState: "No quota or budget rows matched the current filters.",
  }),
  "traffic-shaping-overview": meta("traffic-shaping-overview", {
    shortDescription: "Summarizes caller/server traffic-shaping decisions.",
    purpose: "Use it to distinguish queued or rejected bursts from hard quotas and upstream limits.",
    dataSemantics: "Top-N aggregate by shaping decision, scope, or bucket.",
    commonFilters: shapingHelpFilters,
    keyColumns: [
      { key: "requests", description: "Traffic-shaping events in this bucket." },
      { key: "queued", description: "Requests admitted after bounded queue wait." },
      { key: "rejections", description: "Requests rejected by shaping before upstream calls." },
    ],
    caveats: "Traffic shaping is configured policy, not provider-side throttling.",
    relatedReports: ["traffic-shaping-by-user", "provider-capacity-shaping", "requests"],
    emptyState: "No traffic-shaping rows matched the current filters.",
  }),
  "traffic-shaping-by-user": meta("traffic-shaping-by-user", {
    shortDescription: "Breaks caller traffic-shaping events down by user.",
    purpose: "Use it to identify users causing shaped bursts or queue delays.",
    dataSemantics: "Top-N aggregate by user and shaping bucket.",
    commonFilters: shapingHelpFilters,
    keyColumns: [
      { key: "key", description: "User bucket." },
      { key: "avgQueueWaitMs", description: "Average bounded queue wait." },
      { key: "avgRetryAfterMs", description: "Average retry-after returned for rejected events." },
    ],
    caveats: "A queued decision can explain added latency without being an error.",
    relatedReports: ["usage-by-caller", "traffic-shaping-overview", "requests"],
    emptyState: "No user shaping rows matched the current filters.",
  }),
  "traffic-shaping-by-key": meta("traffic-shaping-by-key", {
    shortDescription: "Breaks caller traffic-shaping events down by key.",
    purpose: "Use it to find bursty keys and review per-key shaping policies.",
    dataSemantics: "Top-N aggregate by public key and shaping bucket.",
    commonFilters: shapingHelpFilters,
    keyColumns: [
      { key: "key", description: "Public token ID or safe key label." },
      { key: "rejections", description: "Shape rejections for the key." },
      { key: "totalReservedTokens", description: "Estimated input plus reserved output tokens evaluated by shaping." },
    ],
    caveats: "Raw tokens and token hashes are never shown.",
    relatedReports: ["usage-by-key", "quotas-budgets", "requests"],
    emptyState: "No key shaping rows matched the current filters.",
  }),
  "traffic-shaping-by-client": meta("traffic-shaping-by-client", {
    shortDescription: "Breaks caller traffic-shaping events down by client.",
    purpose: "Use it to identify bursty CLI, SDK, or application clients.",
    dataSemantics: "Top-N aggregate by client and shaping bucket.",
    commonFilters: shapingHelpFilters,
    keyColumns: [
      { key: "key", description: "Client bucket." },
      { key: "queued", description: "Queued events for the client." },
      { key: "p95QueueWaitMs", description: "P95 bounded queue wait." },
    ],
    caveats: "Client labels depend on safe stored client metadata.",
    relatedReports: ["client-breakdown", "traffic-shaping-overview", "requests"],
    emptyState: "No client shaping rows matched the current filters.",
  }),
  "traffic-shaping-by-group": meta("traffic-shaping-by-group", {
    shortDescription: "Breaks caller traffic-shaping events down by model group.",
    purpose: "Use it to identify groups whose traffic pattern needs different shaping or quota policy.",
    dataSemantics: "Top-N aggregate by model group and shaping bucket.",
    commonFilters: shapingHelpFilters,
    keyColumns: [
      { key: "key", description: "Resolved model group bucket." },
      { key: "estimatedInputTokens", description: "Estimated input tokens evaluated by shaping." },
      { key: "reservedOutputTokens", description: "Requested output cap reserved by shaping." },
    ],
    caveats: "Model-group names are deployment-defined.",
    relatedReports: ["groups", "max-token-buckets", "requests"],
    emptyState: "No group shaping rows matched the current filters.",
  }),
  "provider-capacity-shaping": meta("provider-capacity-shaping", {
    shortDescription: "Shows provider/model/target shared-capacity shaping decisions.",
    purpose: "Use it to diagnose locally skipped or throttled upstream targets before provider calls.",
    dataSemantics: "Top-N aggregate by provider shaping scope or bucket.",
    commonFilters: shapingHelpFilters,
    keyColumns: [
      { key: "skippedTargets", description: "Eligible targets skipped by provider/model/target shaping." },
      { key: "upstream429Attempts", description: "Upstream attempts that returned rate-limit status." },
      { key: "routeAroundSuccesses", description: "Requests that succeeded after avoiding a shaped target." },
    ],
    caveats: "Provider shaping is local admission control; inspect attempts for upstream-returned 429s.",
    relatedReports: ["adaptive-upstream-backoff", "provider-model-mix", "requests"],
    emptyState: "No provider-shaping rows matched the current filters.",
  }),
  "adaptive-upstream-backoff": meta("adaptive-upstream-backoff", {
    shortDescription: "Shows adaptive cooldowns started after upstream rate-limit or quota signals.",
    purpose: "Use it to confirm whether previous upstream failures are causing temporary route-around behavior.",
    dataSemantics: "Top-N aggregate by adaptive backoff bucket.",
    commonFilters: shapingHelpFilters,
    keyColumns: [
      { key: "cooldownsStarted", description: "Adaptive cooldown windows started for provider/model/target buckets." },
      { key: "upstreamQuotaAttempts", description: "Attempts classified as provider quota or billing exhaustion." },
      { key: "avgRetryAfterMs", description: "Average retry-after or cooldown guidance." },
    ],
    caveats: "Cooldowns should be reviewed with provider status and recent request attempts.",
    relatedReports: ["provider-capacity-shaping", "errors-fallbacks", "requests"],
    emptyState: "No adaptive-backoff rows matched the current filters.",
  }),
  "traffic-tuning-advisor": meta("traffic-tuning-advisor", {
    shortDescription: "Recommends traffic-shaping and provider-capacity tuning actions from safe usage telemetry.",
    purpose: "Use it to decide whether user-facing errors call for caller burst changes, bounded queue changes, client throttling, provider shaping, or request-shape route-around.",
    dataSemantics: "Deterministic top-N recommendation rows grouped by user, project, client, group, caller, environment, provider, model, and dialect.",
    commonFilters: [...shapingHelpFilters, "status", "cache"],
    keyColumns: [
      { key: "recommendation", description: "Conservative action class such as enable_queue or route_around_incompatible_target." },
      { key: "observedValues", description: "Safe scalar counts and p95 values that triggered the recommendation." },
      { key: "configFields", description: "Configuration fields to inspect before making an operator-approved change." },
    ],
    caveats: "The advisor never edits production config. Validate suggested changes with request reports and smokes before rollout.",
    relatedReports: ["traffic-shaping-overview", "provider-capacity-shaping", "upstream-failures", "request-shape-failures"],
    emptyState: "No advisor rows matched the current filters.",
  }),
  "troubleshooting-buckets": meta("troubleshooting-buckets", {
    shortDescription: "Groups deterministic troubleshooting signals into operational buckets.",
    purpose: "Use it to quickly classify failures by quota, rate limit, key state, cache, fallback, or HTTP class.",
    dataSemantics: "Top-N aggregate by troubleshooting bucket.",
    commonFilters: [...baseFilters, "sort", "direction"],
    keyColumns: [
      { key: "key", description: "Troubleshooting bucket." },
      { key: "errors", description: "Error rows represented by the bucket." },
      { key: "fallbacks", description: "Fallback rows represented by the bucket." },
    ],
    caveats: "Buckets are deterministic summaries, not root-cause proof.",
    relatedReports: ["errors-fallbacks", "requests", "quotas-budgets"],
    emptyState: "No troubleshooting buckets matched the current filters.",
  }),
  "routing-decisions": meta("routing-decisions", {
    shortDescription: "Shows safe routing strategy and decision buckets.",
    purpose: "Use it to understand which routing strategies and outcomes selected targets.",
    dataSemantics: "Top-N aggregate by routing decision bucket.",
    commonFilters: [...baseFilters, "sort", "direction"],
    keyColumns: [
      { key: "key", description: "Routing decision or strategy bucket." },
      { key: "requests", description: "Requests represented by the decision bucket." },
      { key: "fallbacks", description: "Requests with fallback transitions after selection." },
    ],
    caveats: "Decision telemetry excludes raw prompts and policy request/response payloads.",
    relatedReports: ["dynamic-signals", "provider-model-mix", "requests"],
    emptyState: "No routing-decision rows matched the current filters.",
  }),
  "dynamic-signals": meta("dynamic-signals", {
    shortDescription: "Shows enabled dynamic-score signal names and usage.",
    purpose: "Use it to confirm which safe signals are influencing target scoring.",
    dataSemantics: "Top-N aggregate by signal name.",
    commonFilters: [...baseFilters, "sort", "direction"],
    keyColumns: [
      { key: "key", description: "Dynamic signal name." },
      { key: "requests", description: "Requests where the signal was present." },
      { key: "secondaryKey", description: "Optional signal bucket or score term." },
    ],
    caveats: "Signal labels must be safe bounded names, not raw prompt content.",
    relatedReports: ["dynamic-score-buckets", "dynamic-thresholds", "routing-decisions"],
    emptyState: "No dynamic-signal rows matched the current filters.",
  }),
  "dynamic-score-buckets": meta("dynamic-score-buckets", {
    shortDescription: "Shows dynamic-score value and final-score buckets.",
    purpose: "Use it to understand how dynamic scoring distributed eligible targets.",
    dataSemantics: "Top-N aggregate by score bucket.",
    commonFilters: [...baseFilters, "sort", "direction"],
    keyColumns: [
      { key: "key", description: "Score or final-score bucket." },
      { key: "requests", description: "Requests represented by the score bucket." },
      { key: "secondaryKey", description: "Optional signal or provider/model bucket." },
    ],
    caveats: "Scores explain routing only when decision telemetry is enabled.",
    relatedReports: ["dynamic-signals", "dynamic-thresholds", "routing-decisions"],
    emptyState: "No dynamic-score rows matched the current filters.",
  }),
  "dynamic-thresholds": meta("dynamic-thresholds", {
    shortDescription: "Shows dynamic-score threshold and filter buckets.",
    purpose: "Use it to see which thresholds removed or admitted candidate targets.",
    dataSemantics: "Top-N aggregate by threshold bucket.",
    commonFilters: [...baseFilters, "sort", "direction"],
    keyColumns: [
      { key: "key", description: "Threshold or filter bucket." },
      { key: "requests", description: "Requests affected by the threshold bucket." },
      { key: "errors", description: "Failures associated with the bucket." },
    ],
    caveats: "Threshold buckets are bounded labels and do not contain raw policy inputs.",
    relatedReports: ["dynamic-score-buckets", "admission-reasons", "requests"],
    emptyState: "No dynamic-threshold rows matched the current filters.",
  }),
  "max-token-buckets": meta("max-token-buckets", {
    shortDescription: "Shows requested output-cap buckets and max-token eligibility effects.",
    purpose: "Use it to diagnose capped requests, large output reservations, and targets that skip capped traffic.",
    dataSemantics: "Top-N aggregate by max-token bucket.",
    commonFilters: [...baseFilters, "sort", "direction"],
    keyColumns: [
      { key: "key", description: "Requested output-cap bucket." },
      { key: "totalTokens", description: "Actual token usage in the bucket." },
      { key: "errors", description: "Errors associated with the bucket." },
    ],
    caveats: "A small cap can change model eligibility or reasoning-model behavior.",
    relatedReports: ["input-token-buckets", "admission-reasons", "requests"],
    emptyState: "No max-token bucket rows matched the current filters.",
  }),
  "input-token-buckets": meta("input-token-buckets", {
    shortDescription: "Shows input-token size buckets for request-shape triage.",
    purpose: "Use it to spot large-context workloads and TPM pressure.",
    dataSemantics: "Top-N aggregate by input-token bucket.",
    commonFilters: [...baseFilters, "sort", "direction"],
    keyColumns: [
      { key: "key", description: "Input-token bucket." },
      { key: "inputTokens", description: "Input tokens in the bucket." },
      { key: "errors", description: "Errors associated with large or small input buckets." },
    ],
    caveats: "Input token estimates and provider-reported usage can differ by dialect.",
    relatedReports: ["max-token-buckets", "quotas-budgets", "requests"],
    emptyState: "No input-token bucket rows matched the current filters.",
  }),
  "admission-reasons": meta("admission-reasons", {
    shortDescription: "Shows admission and no-eligible-target reason buckets.",
    purpose: "Use it to explain why requests or targets were admitted, skipped, or rejected.",
    dataSemantics: "Top-N aggregate by admission reason.",
    commonFilters: [...baseFilters, "sort", "direction"],
    keyColumns: [
      { key: "key", description: "Admission or filter reason bucket." },
      { key: "requests", description: "Requests represented by the reason." },
      { key: "errors", description: "Failures associated with the reason." },
    ],
    caveats: "Reasons are safe bounded labels and do not include raw tool schemas or prompts.",
    relatedReports: ["provider-catalog-status", "contract-buckets", "requests"],
    emptyState: "No admission reason rows matched the current filters.",
  }),
  "provider-catalog-status": meta("provider-catalog-status", {
    shortDescription: "Shows safe provider catalog, active-target metadata, and effective provider-skin eligibility.",
    purpose: "Use it to review active/catalog-only status, pricing, modalities, tools, validation freshness, and whether catalog capabilities are active for the resolved provider skin.",
    dataSemantics: "Top-N catalog/status rows with safe runtime metadata.",
    commonFilters: [...baseFilters, "sort", "direction"],
    keyColumns: [
      { key: "validationStatus", description: "Current validation state for the catalog row or active target." },
      { key: "activeTargetCount", description: "Number of active targets using this model metadata." },
      { key: "activeEligibilitySkin", description: "Resolved native provider skin used by the active target." },
      { key: "inactiveToolSupport", description: "Catalog tool metadata for skins that are not active for this target." },
      { key: "pricingMissing", description: "Whether required known pricing metadata is absent." },
    ],
    caveats: "Provider keys, headers, full config, and private paths are not exposed.",
    relatedReports: ["target-validation", "contract-buckets", "provider-model-mix"],
    emptyState: "No provider catalog rows matched the current filters.",
  }),
  "retention-status": meta("retention-status", {
    shortDescription: "Shows read-only retention and rollup status.",
    purpose: "Use it to confirm retention dry-run counts, legal holds, blocked rows, and rollup coverage.",
    dataSemantics: "Current retention status rows from usage DB metadata.",
    commonFilters: ["since", "sort", "direction"],
    keyColumns: [
      { key: "candidateRows", description: "Rows matching retention policy before hold/block checks." },
      { key: "heldRows", description: "Rows protected by legal hold." },
      { key: "blockedRows", description: "Rows not eligible for deletion or not implemented." },
    ],
    caveats: "The tab is read-only; retention execution remains an operator workflow.",
    relatedReports: ["requests", "security-events", "project-chargeback"],
    emptyState: "No retention status rows are available for this deployment.",
  }),
  "contract-buckets": meta("contract-buckets", {
    shortDescription: "Shows model-group contract pass/fail and reason buckets.",
    purpose: "Use it to validate whether groups meet deployment-defined quality, modality, tool, and policy contracts.",
    dataSemantics: "Top-N aggregate by contract bucket.",
    commonFilters: [...baseFilters, "sort", "direction"],
    keyColumns: [
      { key: "key", description: "Contract status or reason bucket." },
      { key: "requests", description: "Requests evaluated against the contract bucket." },
      { key: "errors", description: "Failures associated with the bucket." },
    ],
    caveats: "Contract buckets do not expose request content or customer data.",
    relatedReports: ["contract-workloads", "target-validation", "requests"],
    emptyState: "No contract bucket rows matched the current filters.",
  }),
  "contract-workloads": meta("contract-workloads", {
    shortDescription: "Shows contract outcomes by deployment-defined workload label.",
    purpose: "Use it to compare group quality and behavior across validated workload classes.",
    dataSemantics: "Top-N aggregate by workload and contract bucket.",
    commonFilters: [...baseFilters, "sort", "direction"],
    keyColumns: [
      { key: "key", description: "Workload or workload/contract bucket." },
      { key: "requests", description: "Requests in the workload bucket." },
      { key: "fallbacks", description: "Fallbacks observed for the workload." },
    ],
    caveats: "Workload labels must be safe deployment-defined labels.",
    relatedReports: ["contract-buckets", "target-validation", "provider-catalog-status"],
    emptyState: "No contract workload rows matched the current filters.",
  }),
  "target-validation": meta("target-validation", {
    shortDescription: "Shows target validation status, workload, and age buckets.",
    purpose: "Use it before promoting provider/model targets or diagnosing contract failures.",
    dataSemantics: "Top-N aggregate by validation bucket.",
    commonFilters: [...baseFilters, "sort", "direction"],
    keyColumns: [
      { key: "key", description: "Validation status or target bucket." },
      { key: "requests", description: "Requests associated with validated or stale targets." },
      { key: "errors", description: "Failures associated with validation buckets." },
    ],
    caveats: "Validation metadata is evidence of prior smokes or harness results, not a live smoke.",
    relatedReports: ["provider-catalog-status", "contract-buckets", "requests"],
    emptyState: "No validation rows matched the current filters.",
  }),
  "expensive-requests": meta("expensive-requests", {
    shortDescription: "Lists high-cost request rows for the selected filters.",
    purpose: "Use it to explain spend spikes and export current-page evidence for incident tickets.",
    dataSemantics: "Cursor-paged request rows sorted by stored cost when supported.",
    commonFilters: [...baseFilters, "sort", "direction"],
    keyColumns: [
      { key: "requestId", description: "Safe request identifier for drilldown." },
      { key: "totalCostUsd", description: "Stored request-time total cost." },
      { key: "inputTokens", description: "Input tokens contributing to request cost." },
    ],
    caveats: "CSV exports the current page, not all matching rows.",
    relatedReports: ["savings", "provider-model-mix", "usage-by-caller"],
    emptyState: "No expensive request rows matched the current filters.",
  }),
  "client-breakdown": meta("client-breakdown", {
    shortDescription: "Shows usage, errors, cost, and latency by client label.",
    purpose: "Use it to compare CLI, SDK, browser, and service traffic patterns.",
    dataSemantics: "Top-N aggregate by client.",
    commonFilters: statusFilters,
    keyColumns: [
      { key: "key", description: "Client bucket." },
      { key: "requests", description: "Requests from that client." },
      { key: "avgLatencyMs", description: "Average latency for that client." },
    ],
    caveats: "Client labels are only as accurate as stored client metadata.",
    relatedReports: ["requests", "traffic-shaping-by-client", "usage-by-caller"],
    emptyState: "No client rows matched the current filters.",
  }),
  "project-chargeback": meta("project-chargeback", {
    shortDescription: "Shows usage and cost by caller project.",
    purpose: "Use it for project chargeback, budget review, and ownership reporting.",
    dataSemantics: "Top-N aggregate by project.",
    commonFilters: statusFilters,
    keyColumns: [
      { key: "key", description: "Project bucket." },
      { key: "totalCostUsd", description: "Stored request-time project cost." },
      { key: "upstreamReportedCostUsd", description: "Provider-reported billed cost when available." },
    ],
    caveats: "Project attribution depends on caller/project membership configuration.",
    relatedReports: ["savings-by-project", "usage-by-caller", "requests"],
    emptyState: "No project chargeback rows matched the current filters.",
  }),
  "capability-usage": meta("capability-usage", {
    shortDescription: "Shows usage by capability such as tools, images, streaming, or dialect.",
    purpose: "Use it to confirm which advanced capabilities are driving traffic, cost, or errors.",
    dataSemantics: "Top-N aggregate by capability bucket.",
    commonFilters: statusFilters,
    keyColumns: [
      { key: "key", description: "Capability bucket." },
      { key: "streams", description: "Streaming requests in the bucket." },
      { key: "inputImageTokens", description: "Image input tokens when reported." },
    ],
    caveats: "Capability metadata is safe and does not include raw tool schemas, images, or prompts.",
    relatedReports: ["provider-catalog-status", "requests", "contract-buckets"],
    emptyState: "No capability rows matched the current filters.",
  }),
  anomalies: meta("anomalies", {
    shortDescription: "Shows deterministic operational anomaly buckets.",
    purpose: "Use it to find errors, fallbacks, slow requests, expensive requests, and abnormal key states.",
    dataSemantics: "Top-N aggregate by deterministic anomaly rule.",
    commonFilters: statusFilters,
    keyColumns: [
      { key: "key", description: "Anomaly rule bucket." },
      { key: "errors", description: "Errors represented by the anomaly." },
      { key: "avgLatencyMs", description: "Average latency for rows in the anomaly bucket." },
    ],
    caveats: "This is not machine-learning anomaly detection.",
    relatedReports: ["requests", "errors-fallbacks", "expensive-requests"],
    emptyState: "No anomaly rows matched the current filters.",
  }),
  "security-events": meta("security-events", {
    shortDescription: "Lists safe scalar security and access events.",
    purpose: "Use it to review authorized/forbidden access, admin-report access, caller auth failures, and security outcomes.",
    dataSemantics: "Cursor-paged security event rows sorted by time unless changed.",
    commonFilters: securityHelpFilters,
    keyColumns: [
      { key: "outcome", description: "Allowed, denied, or other bounded security outcome." },
      { key: "reason", description: "Safe bounded reason for the security event." },
      { key: "surface", description: "Router surface where the event occurred." },
    ],
    caveats: "Requires separate security-report authorization and never exposes bearer tokens or token hashes.",
    relatedReports: ["requests", "usage-by-key", "project-chargeback"],
    emptyState: "No security events matched the current filters or security reports are disabled.",
  }),
  requests: meta("requests", {
    shortDescription: "Lists recent safe request rows with request-ID drilldown.",
    purpose: "Use it for row-by-row incident review after identifying a user, provider, error, shaping, or spend dimension.",
    dataSemantics: "Cursor-paged request rows with stable server ordering and filtered totals.",
    commonFilters: [...statusFilters, "sort", "direction"],
    keyColumns: [
      { key: "requestId", description: "Safe request identifier for detail lookup." },
      { key: "status", description: "Caller-visible terminal HTTP status." },
      { key: "latencyMs", description: "End-to-end latency observed by the router." },
    ],
    caveats: "CSV exports the current page. Request detail remains safe scalar diagnostics only.",
    relatedReports: ["errors-fallbacks", "provider-model-mix", "latency-throughput"],
    emptyState: "No request rows matched the current filters. Try a broader time range or remove narrow dimensions.",
  }),
} satisfies Record<string, ReportMetadata>;

function reportMeta(id: keyof typeof reportMetadataById): ReportMetadata {
  return reportMetadataById[id];
}

export const tabSpecs: TabSpec[] = [
  { id: "data-migrations", label: "Data migrations", endpoint: "migrations", metadata: reportMeta("data-migrations"), columns: migrationColumns, filters: ["scope", "release", "state", "type", "date"] },
  { id: "overview", label: "Overview", endpoint: "summary", metadata: reportMeta("overview"), overview: true },
  { id: "groups", label: "Groups", endpoint: "summary", metadata: reportMeta("groups"), columns: defaultScalarColumns, filters: statusCacheFilters },
  { id: "providers", label: "Providers", endpoint: "summary", metadata: reportMeta("providers"), columns: defaultScalarColumns, filters: statusCacheFilters },
  { id: "tokens", label: "Keys", endpoint: "summary", metadata: reportMeta("tokens"), columns: defaultScalarColumns, filters: statusCacheFilters },
  { id: "savings", label: "Savings", endpoint: "savings", metadata: reportMeta("savings"), columns: savingsColumns, savings: true, filters: savingsFilters },
  { id: "savings-by-user", label: "Savings by user", endpoint: "savings-by-user", metadata: reportMeta("savings-by-user"), columns: savingsBreakdownColumns("User"), savings: true, filters: savingsFilters },
  { id: "savings-by-key", label: "Savings by key", endpoint: "savings-by-key", metadata: reportMeta("savings-by-key"), columns: savingsBreakdownColumns("Key"), savings: true, filters: savingsFilters },
  { id: "savings-by-group", label: "Savings by group", endpoint: "savings-by-group", metadata: reportMeta("savings-by-group"), columns: savingsBreakdownColumns("Model group"), savings: true, filters: savingsFilters },
  { id: "savings-by-project", label: "Savings by project", endpoint: "savings-by-project", metadata: reportMeta("savings-by-project"), columns: savingsBreakdownColumns("Project", "Environment"), savings: true, filters: savingsFilters },
  { id: "savings-by-provider-model", label: "Savings by provider", endpoint: "savings-by-provider-model", metadata: reportMeta("savings-by-provider-model"), columns: savingsBreakdownColumns("Provider/model", "Dialect"), savings: true, filters: savingsFilters },
  { id: "model-groups-by-user", label: "User groups", endpoint: "model-groups-by-user", metadata: reportMeta("model-groups-by-user"), columns: defaultScalarColumns, filters: statusCacheFilters },
  { id: "usage-by-key", label: "Key usage", endpoint: "usage-by-key", metadata: reportMeta("usage-by-key"), columns: defaultScalarColumns, filters: statusCacheFilters },
  { id: "usage-by-caller", label: "Caller usage", endpoint: "usage-by-caller", metadata: reportMeta("usage-by-caller"), columns: defaultScalarColumns, filters: statusCacheFilters },
  { id: "requested-models", label: "Requested models", endpoint: "requested-models", metadata: reportMeta("requested-models"), columns: defaultScalarColumns, filters: statusCacheFilters },
  { id: "provider-model-mix", label: "Provider/model", endpoint: "provider-model-mix", metadata: reportMeta("provider-model-mix"), columns: defaultScalarColumns, filters: statusCacheFilters },
  { id: "latency-throughput", label: "Latency", endpoint: "latency-throughput", metadata: reportMeta("latency-throughput"), columns: [...identityColumns, ...trafficColumns, ...latencyColumns, ...throughputColumns, ...tokenColumns], filters: statusCacheSortFilters },
  { id: "errors-fallbacks", label: "Errors", endpoint: "errors-fallbacks", metadata: reportMeta("errors-fallbacks"), columns: [...identityColumns, ...trafficColumns, ...latencyColumns], filters: statusCacheSortFilters },
  { id: "upstream-failures", label: "Upstream failures", endpoint: "upstream-failures", metadata: reportMeta("upstream-failures"), columns: diagnosticColumns, filters: statusCacheSortFilters },
  { id: "request-shape-failures", label: "Shape failures", endpoint: "request-shape-failures", metadata: reportMeta("request-shape-failures"), columns: diagnosticColumns, filters: statusCacheSortFilters },
  { id: "fallback-health", label: "Fallback health", endpoint: "fallback-health", metadata: reportMeta("fallback-health"), columns: diagnosticColumns, filters: statusCacheSortFilters },
  { id: "user-client-impact", label: "User impact", endpoint: "user-client-impact", metadata: reportMeta("user-client-impact"), columns: diagnosticColumns, filters: statusCacheSortFilters },
  { id: "cache-report", label: "Cache", endpoint: "cache", metadata: reportMeta("cache-report"), columns: [...identityColumns, ...trafficColumns, ...cacheColumns], filters: statusCacheSortFilters },
  { id: "quotas-budgets", label: "Quotas", endpoint: "quotas-budgets", metadata: reportMeta("quotas-budgets"), columns: defaultScalarColumns, filters: ["status"] },
  { id: "traffic-shaping-overview", label: "Shaping", endpoint: "traffic-shaping-overview", metadata: reportMeta("traffic-shaping-overview"), columns: [...identityColumns, ...shapingColumns], filters: shapingFilters },
  { id: "traffic-shaping-by-user", label: "Shaping users", endpoint: "traffic-shaping-by-user", metadata: reportMeta("traffic-shaping-by-user"), columns: [...identityColumns, ...shapingColumns], filters: shapingFilters },
  { id: "traffic-shaping-by-key", label: "Shaping keys", endpoint: "traffic-shaping-by-key", metadata: reportMeta("traffic-shaping-by-key"), columns: [...identityColumns, ...shapingColumns], filters: shapingFilters },
  { id: "traffic-shaping-by-client", label: "Shaping clients", endpoint: "traffic-shaping-by-client", metadata: reportMeta("traffic-shaping-by-client"), columns: [...identityColumns, ...shapingColumns], filters: shapingFilters },
  { id: "traffic-shaping-by-group", label: "Shaping groups", endpoint: "traffic-shaping-by-group", metadata: reportMeta("traffic-shaping-by-group"), columns: [...identityColumns, ...shapingColumns], filters: shapingFilters },
  { id: "provider-capacity-shaping", label: "Provider shaping", endpoint: "provider-capacity-shaping", metadata: reportMeta("provider-capacity-shaping"), columns: [...identityColumns, ...shapingColumns], filters: shapingFilters },
  { id: "adaptive-upstream-backoff", label: "Backoff", endpoint: "adaptive-upstream-backoff", metadata: reportMeta("adaptive-upstream-backoff"), columns: [...identityColumns, ...shapingColumns], filters: shapingFilters },
  { id: "traffic-tuning-advisor", label: "Tuning advisor", endpoint: "traffic-tuning-advisor", metadata: reportMeta("traffic-tuning-advisor"), columns: trafficTuningColumns, filters: [...shapingFilters, "status", "cache"] },
  { id: "troubleshooting-buckets", label: "Troubleshooting", endpoint: "troubleshooting-buckets", metadata: reportMeta("troubleshooting-buckets"), columns: defaultScalarColumns, filters: sortDirectionFilters },
  { id: "routing-decisions", label: "Routing", endpoint: "routing-decisions", metadata: reportMeta("routing-decisions"), columns: defaultScalarColumns, filters: sortDirectionFilters },
  { id: "dynamic-signals", label: "Dynamic signals", endpoint: "dynamic-signals", metadata: reportMeta("dynamic-signals"), columns: defaultScalarColumns, filters: sortDirectionFilters },
  { id: "dynamic-score-buckets", label: "Dynamic scores", endpoint: "dynamic-score-buckets", metadata: reportMeta("dynamic-score-buckets"), columns: defaultScalarColumns, filters: sortDirectionFilters },
  { id: "dynamic-thresholds", label: "Dynamic thresholds", endpoint: "dynamic-thresholds", metadata: reportMeta("dynamic-thresholds"), columns: defaultScalarColumns, filters: sortDirectionFilters },
  { id: "max-token-buckets", label: "Max tokens", endpoint: "max-token-buckets", metadata: reportMeta("max-token-buckets"), columns: defaultScalarColumns, filters: sortDirectionFilters },
  { id: "input-token-buckets", label: "Input tokens", endpoint: "input-token-buckets", metadata: reportMeta("input-token-buckets"), columns: defaultScalarColumns, filters: sortDirectionFilters },
  { id: "admission-reasons", label: "Admission", endpoint: "admission-reasons", metadata: reportMeta("admission-reasons"), columns: defaultScalarColumns, filters: sortDirectionFilters },
  { id: "provider-catalog-status", label: "Catalog status", endpoint: "provider-catalog-status", metadata: reportMeta("provider-catalog-status"), columns: catalogColumns, filters: sortDirectionFilters },
  { id: "retention-status", label: "Retention", endpoint: "retention-status", metadata: reportMeta("retention-status"), columns: retentionColumns, filters: sortDirectionFilters },
  { id: "contract-buckets", label: "Contracts", endpoint: "contract-buckets", metadata: reportMeta("contract-buckets"), columns: defaultScalarColumns, filters: sortDirectionFilters },
  { id: "contract-workloads", label: "Workloads", endpoint: "contract-workloads", metadata: reportMeta("contract-workloads"), columns: defaultScalarColumns, filters: sortDirectionFilters },
  { id: "target-validation", label: "Validation", endpoint: "target-validation", metadata: reportMeta("target-validation"), columns: defaultScalarColumns, filters: sortDirectionFilters },
  { id: "expensive-requests", label: "Expensive", endpoint: "expensive-requests", metadata: reportMeta("expensive-requests"), columns: requestColumns, requests: true, filters: sortDirectionFilters },
  { id: "client-breakdown", label: "Clients", endpoint: "client-breakdown", metadata: reportMeta("client-breakdown"), columns: defaultScalarColumns, filters: statusCacheFilters },
  { id: "project-chargeback", label: "Projects", endpoint: "project-chargeback", metadata: reportMeta("project-chargeback"), columns: defaultScalarColumns, filters: statusCacheFilters },
  { id: "capability-usage", label: "Capabilities", endpoint: "capability-usage", metadata: reportMeta("capability-usage"), columns: defaultScalarColumns, filters: statusCacheFilters },
  { id: "anomalies", label: "Anomalies", endpoint: "anomalies", metadata: reportMeta("anomalies"), columns: defaultScalarColumns, filters: statusCacheFilters },
  { id: "security-events", label: "Security", endpoint: "security/events", metadata: reportMeta("security-events"), columns: securityColumns, security: true, filters: sortDirectionFilters },
  { id: "requests", label: "Requests", endpoint: "requests", metadata: reportMeta("requests"), columns: requestColumns, requests: true, filters: statusCacheSortFilters },
];

export function tabById(id: string): TabSpec | undefined {
  return tabSpecs.find((tab) => tab.id === id);
}

export async function fetchReport(endpoint: string, filters: ReportFilters): Promise<ReportResponse> {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(filters)) {
    if (value.trim()) params.set(key, value.trim());
  }
  const res = await fetch(`api/${endpoint}?${params}`, { credentials: "same-origin" });
  if (!res.ok) throw new Error(await reportErrorMessage(endpoint, res));
  return (await res.json()) as ReportResponse;
}

async function reportErrorMessage(endpoint: string, res: Response) {
  try {
    const body = (await res.json()) as { error?: { message?: string; type?: string } };
    const message = body.error?.message || body.error?.type || "";
    if (message) return `${endpoint} failed: ${message}`;
  } catch {
    // Fall through to the HTTP status when the server did not return JSON.
  }
  return `${endpoint} failed with HTTP ${res.status}`;
}

export async function fetchVersion(): Promise<VersionResponse> {
  const res = await fetch("api/version", {
    credentials: "same-origin",
    headers: { Accept: "application/json" },
  });
  if (!res.ok) throw new Error(`version failed with HTTP ${res.status}`);
  return (await res.json()) as VersionResponse;
}

export function rowsForTab(tab: TabSpec, report: ReportResponse): ReportRow[] {
  if (tab.id === "groups") return report.byGroup || [];
  if (tab.id === "providers") return report.byProvider || [];
  if (tab.id === "tokens") return report.byToken || [];
  if (tab.id === "savings") {
    const rows: ReportRow[] = [];
    if (report.summary && Object.keys(report.summary).length > 0) rows.push(report.summary as ReportRow);
    rows.push(...(report.byGroup || []), ...(report.byTime || []));
    return rows;
  }
  if (tab.requests || tab.id === "requests") return report.requests || [];
  if (isSavingsBreakdownTab(tab)) return normalizeSavingsBreakdownRows(report.rows || report.byTime || []);
  if (tab.id === "retention-status") {
    const retention = report as ReportResponse & { tables?: ReportRow[]; rollups?: ReportRow[] };
    return [...(retention.tables || []), ...(retention.rollups || [])];
  }
  return report.rows || report.byTime || [];
}

export function columnsForTab(tab: TabSpec, rows: ReportRow[]): ReportColumn[] {
  const schema = tab.columns?.length ? tab.columns : inferColumns(rows);
  const available = new Set<string>();
  for (const row of rows) {
    for (const key of Object.keys(row)) {
      if (hasVisibleValue(row[key])) available.add(key);
    }
  }
  const selected = schema.filter((column) => available.has(column.key) || (isSavingsBreakdownTab(tab) && columnAliasesVisible(column.key, rows)));
  const seen = new Set<string>();
  const deduped = selected.filter((column) => {
    if (seen.has(column.key)) return false;
    seen.add(column.key);
    return true;
  });
  return describeColumns(suppressRedundantAliases(deduped, rows));
}

function isSavingsBreakdownTab(tab: TabSpec): boolean {
  return tab.savings === true && tab.id.startsWith("savings-by-");
}

function normalizeSavingsBreakdownRows(rows: ReportRow[]): ReportRow[] {
  return rows.map((row) => ({
    ...row,
    inputTokens: firstVisibleValue(row, ["inputTokens", "input_tokens"]),
    outputTokens: firstVisibleValue(row, ["outputTokens", "output_tokens"]),
    totalTokens: firstVisibleValue(row, ["totalTokens", "total_tokens", "tokens"]),
    actualCostUsd: firstVisibleValue(row, ["actualCostUsd", "actual_cost_usd", "totalCostUsd", "costUsd"]),
    baselineCostUsd: firstVisibleValue(row, ["baselineCostUsd", "baseline_cost_usd"]),
    savingsUsd: firstVisibleValue(row, ["savingsUsd", "savings_usd"]),
    savingsPct: firstVisibleValue(row, ["savingsPct", "savings_pct"]),
    avgCostUsd: firstVisibleValue(row, ["avgCostUsd", "avg_cost_usd"]),
  }));
}

function firstVisibleValue(row: ReportRow, keys: string[]): unknown {
  for (const key of keys) {
    if (hasVisibleValue(row[key])) return row[key];
  }
  return undefined;
}

const columnDescriptions: Record<string, string> = {
  key: "Primary report bucket for the selected tab.",
  secondaryKey: "Secondary bucket used when the report groups by two dimensions.",
  status: "Caller-visible status or upstream attempt status for diagnostic rows.",
  errorClass: "Safe bounded router error class from attempts, terminal errors, or fallback transitions.",
  provider: "Upstream provider label from safe telemetry.",
  model: "Upstream model label from safe telemetry.",
  dialect: "Upstream API dialect or router skin.",
  requestShapeFingerprint: "Non-reversible request-shape fingerprint for comparing repeated request structures.",
  toolSchemaFingerprint: "Non-reversible tool-schema fingerprint for comparing repeated tool shapes.",
  requests: "Requests counted in this report bucket.",
  attempts: "Upstream provider/model attempts, including retries or fallbacks.",
  retryableAttempts: "Failed attempts classified as retryable by router diagnostics.",
  timeoutAttempts: "Failed attempts classified as upstream timeouts.",
  streams: "Requests that used a streaming response path.",
  errors: "Requests with caller-visible HTTP status >= 400 or bucketed error outcomes.",
  errorRatePct: "Errors as a percentage of requests in the bucket.",
  fallbacks: "Requests that used one or more alternate upstream attempts.",
  fallbackRatePct: "Fallbacks as a percentage of requests in the bucket.",
  fallbackSucceeded: "Fallback transitions that recovered on a later upstream attempt.",
  fallbackFailed: "Fallback transitions that did not recover before the terminal response.",
  terminalErrors: "Terminal request error rows persisted for affected requests.",
  upstreamErrorDetails: "Allowlisted sanitized provider error detail fields persisted for failed attempts.",
  fieldsStrippedCount: "Translated upstream fields stripped by router translation.",
  fieldsRewrittenCount: "Translated upstream fields rewritten by router translation.",
  unsupportedFieldsCount: "Unsupported translated fields detected in safe translation telemetry.",
  translationWarningCount: "Safe translation warning count for upstream attempts.",
  affectedUsers: "Distinct safe caller-user labels in the bucket.",
  affectedClients: "Distinct safe client labels in the bucket.",
  inputTokens: "Input tokens reported or estimated for completed requests.",
  outputTokens: "Output tokens reported for completed requests.",
  inputImageTokens: "Image input tokens reported by the upstream when available.",
  totalTokens: "Input plus output tokens for completed requests.",
  input_tokens: "Input tokens in snake_case savings and export rows.",
  output_tokens: "Output tokens in snake_case savings and export rows.",
  total_tokens: "Total tokens in snake_case savings and export rows.",
  inputCostUsd: "Stored request-time calculated input-token cost.",
  imageCostUsd: "Stored request-time calculated image-input cost when separately priced.",
  outputCostUsd: "Stored request-time calculated output-token cost.",
  totalCostUsd: "Stored request-time calculated total cost.",
  actual_cost_usd: "Stored request-time actual cost used by savings reports.",
  actualCostUsd: "Stored request-time actual cost used by savings reports.",
  baseline_cost_usd: "Hypothetical cost from the selected source-dated baseline.",
  baselineCostUsd: "Hypothetical cost from the selected source-dated baseline.",
  savings_usd: "Baseline cost minus stored actual cost.",
  savingsUsd: "Baseline cost minus stored actual cost.",
  savings_pct: "Savings as a percentage of baseline cost.",
  savingsPct: "Savings as a percentage of baseline cost.",
  avgCostUsd: "Average stored total cost per request.",
  upstreamReportedCostUsd: "Upstream-reported billed cost when the provider returns it.",
  avgLatencyMs: "Average caller-observed end-to-end latency.",
  maxLatencyMs: "Maximum caller-observed end-to-end latency.",
  avgTtfbMs: "Average time to first byte.",
  maxTtfbMs: "Maximum time to first byte.",
  avgUpstreamMs: "Average upstream provider duration.",
  maxUpstreamMs: "Maximum upstream provider duration.",
  avgDownstreamMs: "Average downstream write duration.",
  maxDownstreamMs: "Maximum downstream write duration.",
  avgUpstreamTokensPerSec: "Average upstream token throughput.",
  avgUpstreamOutputTokensPerSec: "Average upstream output-token throughput.",
  avgUpstreamTotalTokensPerSec: "Average upstream total-token throughput.",
  avgDownstreamTokensPerSec: "Average downstream token throughput.",
  avgDownstreamWriteOutputTokensPerSec: "Average downstream output-token write throughput.",
  avgDownstreamWriteTotalTokensPerSec: "Average downstream total-token write throughput.",
  cacheHits: "Requests served from cache without an upstream provider call.",
  cacheMisses: "Cacheable requests that did not find a stored response.",
  cacheBypass: "Requests intentionally bypassed by request shape or policy.",
  cacheHitRatePct: "Cache hits as a percentage of cache-eligible rows in the bucket.",
  latestCacheItems: "Latest observed item count in the response cache.",
  latestCacheBytes: "Latest observed occupied cache bytes.",
  latestCacheMaxBytes: "Configured cache capacity in bytes.",
  latestCacheOccupancyPct: "Latest observed cache occupancy percentage.",
  rejections: "Traffic-shaping events rejected before an upstream call.",
  queued: "Traffic-shaping events admitted after bounded queue wait.",
  skippedTargets: "Eligible provider/model/target candidates skipped by shared-capacity shaping.",
  cooldownsStarted: "Adaptive backoff cooldown windows started.",
  avgRetryAfterMs: "Average retry-after or cooldown guidance.",
  maxRetryAfterMs: "Maximum retry-after or cooldown guidance.",
  avgQueueWaitMs: "Average bounded queue wait before admission or rejection.",
  p50QueueWaitMs: "Median bounded queue wait.",
  p95QueueWaitMs: "P95 bounded queue wait.",
  maxQueueWaitMs: "Maximum bounded queue wait.",
  estimatedInputTokens: "Estimated input tokens evaluated for shaping or admission.",
  reservedOutputTokens: "Caller-requested output cap reserved for shaping or quota.",
  totalReservedTokens: "Estimated input tokens plus reserved output tokens.",
  upstream429Attempts: "Upstream attempts that returned rate-limit status.",
  upstreamQuotaAttempts: "Upstream attempts classified as provider quota or billing exhaustion.",
  routeAroundSuccesses: "Requests that succeeded after avoiding a shaped or backed-off target.",
  recommendation: "Deterministic advisor action class for the selected traffic bucket.",
  severity: "Conservative severity bucket based on observed rates.",
  router429: "Caller-visible 429 responses from router-side policy or shaping.",
  trafficShaped: "Requests with caller traffic-shaping telemetry.",
  upstream400: "Upstream attempts that returned invalid-request or other 4xx status except 429.",
  upstream5xx: "Upstream attempts that returned 5xx status.",
  upstreamTimeouts: "Upstream attempts that timed out.",
  clientCanceled: "Attempts canceled by the downstream client.",
  observedValues: "Compact safe evidence string used by the advisor rule.",
  threshold: "Rule threshold that triggered the advisor recommendation.",
  configFields: "Configuration fields to inspect before making an operator-approved change.",
  explanation: "Human-readable reason for the advisor recommendation.",
  timeUtc: "UTC timestamp for the row.",
  requestId: "Safe request identifier used for request drilldown.",
  callerId: "Configured caller identifier, not a raw token.",
  callerUser: "Configured caller owner user when present.",
  project: "Configured project attribution when present.",
  environment: "Configured environment attribution when present.",
  callerIp: "Stored caller IP when IP storage is enabled and trusted proxy rules apply.",
  tokenId: "Public token ID or safe key label; raw tokens and hashes are never shown.",
  client: "Safe client label such as CLI, SDK, or application.",
  requestedModel: "Model group value requested by the caller.",
  modelGroup: "Resolved deployment-defined model group.",
  cache: "Cache state such as hit, miss, bypass, or disabled.",
  fallback: "Whether the request used fallback to another upstream attempt.",
  costUsd: "Stored request-time total cost alias for request rows.",
  latencyMs: "End-to-end latency observed by the router.",
  ttfbMs: "Time to first byte for the request.",
  upstreamMs: "Upstream provider duration.",
  downstreamMs: "Downstream write duration.",
  error: "Sanitized terminal error class or message.",
  eventType: "Safe security event type.",
  surface: "Router surface where the security event occurred.",
  method: "HTTP method for the security event.",
  path: "Safe request path for the security event.",
  outcome: "Allowed, denied, or other bounded security outcome.",
  reason: "Safe bounded reason for the event or decision.",
  authSubject: "Authenticated admin or caller subject when safe to display.",
  authSource: "Authentication source such as Basic, OIDC, or caller token.",
  adminSubject: "Authenticated admin subject when safe to display.",
  userAgentFamily: "Bounded user-agent family label.",
  ipAddress: "Stored IP address when enabled and allowed by deployment policy.",
  ipSource: "How the stored IP address was derived.",
  resolvedGroup: "Resolved deployment-defined model group.",
  dataClass: "Retention data class.",
  tableName: "Usage database table included in retention status.",
  rollupType: "Rollup granularity such as hourly, daily, or monthly.",
  retentionDays: "Configured retention period in days.",
  candidateRows: "Rows matching the retention policy before hold/block checks.",
  eligibleRows: "Rows eligible for the current retention action.",
  heldRows: "Rows protected by legal hold.",
  blockedRows: "Rows blocked from deletion.",
  deletedRows: "Rows deleted by an approved retention run.",
  sourceRequestCount: "Source request count represented by a rollup.",
  dailyRows: "Daily rollup rows in the status bucket.",
  rollupRows: "Rollup rows in the status bucket.",
  windowStart: "UTC start of the rollup or retention window.",
  windowEnd: "UTC end of the rollup or retention window.",
  completedAt: "UTC completion time for the job or rollup.",
  message: "Safe status message.",
  source: "Catalog row source such as catalog metadata or active target.",
  modelRef: "Provider catalog model reference.",
  activeGroups: "Deployment-defined model groups using the target.",
  activeTargetCount: "Number of active targets using this metadata.",
  validationStatus: "Validation state for catalog row or active target.",
  validationWorkload: "Safe workload label used for validation.",
  validationAgeBucket: "Bounded age bucket for validation evidence.",
  qualityScore: "Configured or reported quality score when available.",
  passRate: "Validation pass rate percentage when available.",
  contextTokens: "Configured or known context window in tokens.",
  inputModalities: "Declared validated input modalities.",
  outputModalities: "Declared output modalities.",
  toolSupport: "Validated tool-support labels by dialect or skin.",
  activeEligibilitySkin: "Resolved provider skin for an active target, prefixed with native unless a bridge surface is explicitly reported.",
  effectiveToolSupport: "Tool-support labels that are active for the resolved provider skin.",
  inactiveToolSupport: "Tool-support labels declared for other skins that are metadata-only for this active target.",
  effectiveStructuredOutputs: "Whether structured-output metadata is active for the resolved provider skin.",
  effectiveReasoning: "Whether reasoning metadata can be applied through the resolved provider skin.",
  effectiveImageInput: "Whether image input is active for this resolved target.",
  eligibilityWarning: "Safe bounded warning for metadata that is not active routing eligibility.",
  forceStoreFalse: "Whether this catalog row or resolved target injects store:false for supported OpenAI-compatible upstream calls.",
  outputTokenField: "OpenAI Chat output-token cap field sent upstream, usually max_tokens or max_completion_tokens.",
  inputPricePerMillionUsd: "Input price per million tokens from source-dated metadata.",
  outputPricePerMillionUsd: "Output price per million tokens from source-dated metadata.",
  pricingSource: "Provider or metadata source used for pricing.",
  pricingUpdatedAt: "Date pricing metadata was last verified.",
  pricingMissing: "Whether required known pricing metadata is missing.",
};

function describeColumns(columns: ReportColumn[]): ReportColumn[] {
  return columns.map((column) => ({
    ...column,
    description: column.description || columnDescriptions[column.key],
  }));
}

function inferColumns(rows: ReportRow[]): ReportColumn[] {
  const seen = new Set<string>();
  for (const row of rows) {
    for (const key of Object.keys(row)) seen.add(key);
  }
  return Array.from(seen)
    .sort((a, b) => a.localeCompare(b))
    .map((key) => ({ key, label: key }));
}

function hasVisibleValue(value: unknown): boolean {
  if (value === null || value === undefined || value === "") return false;
  if (Array.isArray(value)) return value.length > 0;
  return true;
}

function suppressRedundantAliases(columns: ReportColumn[], rows: ReportRow[]): ReportColumn[] {
  const keys = new Set(columns.map((column) => column.key));
  const remove = new Set<string>();
  if (keys.has("tokens") && keys.has("totalTokens") && rowsEveryEqual(rows, "tokens", "totalTokens")) remove.add("tokens");
  if (keys.has("costUsd") && keys.has("totalCostUsd") && rowsEveryEqual(rows, "costUsd", "totalCostUsd")) remove.add("costUsd");
  return columns.filter((column) => !remove.has(column.key));
}

function rowsEveryEqual(rows: ReportRow[], left: string, right: string): boolean {
  return rows.length > 0 && rows.every((row) => String(row[left] ?? "") === String(row[right] ?? ""));
}

const columnAliases: Record<string, string[]> = {
  actualCostUsd: ["actual_cost_usd", "totalCostUsd", "costUsd"],
  baselineCostUsd: ["baseline_cost_usd"],
  savingsUsd: ["savings_usd"],
  savingsPct: ["savings_pct"],
  inputTokens: ["input_tokens"],
  outputTokens: ["output_tokens"],
  totalTokens: ["total_tokens", "tokens"],
  avgCostUsd: ["avg_cost_usd"],
};

function columnAliasesVisible(key: string, rows: ReportRow[]): boolean {
  for (const alias of columnAliases[key] || []) {
    if (rows.some((row) => hasVisibleValue(row[alias]))) return true;
  }
  return false;
}

const sortAliases: Record<string, string[]> = {
  actualCostUsd: ["actual_cost_usd", "costUsd", "totalCostUsd"],
  baselineCostUsd: ["baseline_cost_usd", "baselineCostUsd"],
  costUsd: ["actualCostUsd", "totalCostUsd"],
  totalCostUsd: ["actualCostUsd", "costUsd"],
  savingsUsd: ["savings_usd", "savingsUsd"],
  savingsPct: ["savings_pct", "savingsPct"],
  inputTokens: ["inputTokens", "input_tokens"],
  outputTokens: ["outputTokens", "output_tokens"],
  totalTokens: ["totalTokens", "total_tokens", "tokens"],
};

export function resolveSortKey(requested: string | undefined, rows: ReportRow[], columns: ReportColumn[]): string {
  const raw = (requested || "").trim();
  if (!raw) return "";
  const visibleKeys = new Set(columns.map((column) => column.key));
  if (visibleKeys.has(raw)) return raw;
  for (const alias of sortAliases[raw] || []) {
    if (visibleKeys.has(alias)) return alias;
  }
  const keys = new Set<string>(visibleKeys);
  for (const row of rows) {
    for (const key of Object.keys(row)) keys.add(key);
  }
  if (keys.has(raw)) return raw;
  for (const alias of sortAliases[raw] || []) {
    if (keys.has(alias)) return alias;
  }
  return "";
}
