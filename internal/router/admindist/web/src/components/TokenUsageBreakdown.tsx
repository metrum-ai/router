// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import { Card, CardContent } from "@/components/ui/card";
import type { ReportSummary } from "@/lib/reports";

function number(summary: ReportSummary, key: string) {
  const raw = summary[key] ?? summary[key.replace(/[A-Z]/g, letter => `_${letter.toLowerCase()}`)];
  return typeof raw === "number" ? Math.max(0, raw) : 0;
}
export function tokenUsageParts(summary: ReportSummary) {
  const cached = number(summary, "cachedInputTokens"), writes = number(summary, "cacheWriteInputTokens"), reasoning = number(summary, "reasoningTokens");
  return [
    { label: "Uncached input", value: Math.max(0, number(summary, "inputTokens") - cached - writes), color: "#38bdf8" },
    { label: "Cached input", value: cached, color: "#34d399" },
    { label: "Cache write", value: writes, color: "#fbbf24" },
    { label: "Visible output", value: Math.max(0, number(summary, "outputTokens") - reasoning), color: "#a78bfa" },
    { label: "Reasoning output", value: reasoning, color: "#fb7185" },
  ];
}
export function costUsageParts(summary: ReportSummary) {
  return tokenUsageParts(summary).map((part, index) => ({ ...part, value: number(summary, ["uncachedInputCostUsd", "cachedInputCostUsd", "cacheWriteCostUsd", "visibleOutputCostUsd", "reasoningOutputCostUsd"][index]) }));
}
function Composition({ parts, cost }: { parts: ReturnType<typeof tokenUsageParts>; cost?: boolean }) {
  const total = parts.reduce((sum, part) => sum + part.value, 0);
  return <Card className="panel-gradient col-span-full"><CardContent className="p-4">
    <div className="font-mono text-xs uppercase text-white/58">{cost ? "Billed token cost composition (USD; images excluded)" : "Token composition (not additive cost)"}</div>
    <div role="img" aria-label={parts.map(part => `${part.label}: ${part.value}`).join(", ")} className="my-3 flex h-5 overflow-hidden rounded">
      {parts.map(part => <span key={part.label} title={`${part.label}: ${part.value}`} style={{ backgroundColor: part.color, width: `${total ? 100 * part.value / total : 0}%` }} />)}
    </div>
    <div className="flex flex-wrap gap-4 text-xs text-white/70">{parts.map(part => <span key={part.label}>{part.label}: {cost ? `$${part.value.toFixed(6)}` : part.value.toLocaleString()}</span>)}</div>
  </CardContent></Card>;
}
export function TokenUsageBreakdown({ summary }: { summary?: ReportSummary }) {
  if (!summary || typeof summary.reasoningReportedRequests !== "number") return null;
  const tiles = [
    { label: "Reasoning share", tokens: summary.reasoningTokens, denominator: summary.outputTokens ?? summary.output_tokens, reported: summary.reasoningReportedRequests },
    { label: "Cached input share", tokens: summary.cachedInputTokens, denominator: summary.inputTokens ?? summary.input_tokens, reported: summary.cachedInputReportedRequests },
  ];
  return <>
    {tiles.map(tile => <Card key={tile.label} className="panel-gradient"><CardContent className="p-4">
      <div className="font-display text-2xl text-white">{typeof tile.tokens === "number" && typeof tile.denominator === "number" && tile.denominator >= 0 && typeof tile.reported === "number" && tile.reported > 0 ? tile.denominator > 0 ? `${(100 * tile.tokens / tile.denominator).toFixed(1)}%` : tile.label === "Reasoning share" ? "No output" : "No input" : "Not reported"}</div>
      <div className="mt-1 font-mono text-xs uppercase text-white/58">{tile.label}</div>
      <div className="text-xs text-white/58">{typeof tile.reported === "number" ? tile.reported : 0} / {summary.requests ?? 0} requests reported</div>
    </CardContent></Card>)}
    {typeof summary.historySource === "string" && <div className="col-span-full text-xs text-white/70">History source: {summary.historySource === "finalized_daily_rollups" ? "finalized daily rollups (UTC-day resolution; no raw-row mixing)" : "raw request rows"}</div>}
    <Composition parts={tokenUsageParts(summary)} />
    {typeof summary.costComponentReportedRequests === "number" && summary.costComponentReportedRequests > 0 ? <Composition parts={costUsageParts(summary)} cost /> : <div className="col-span-full text-xs text-white/58">Cost composition unavailable</div>}
    <div className="col-span-full text-xs text-white/58">{summary.costComponentReportedRequests ?? 0} / {summary.requests ?? 0} requests have a complete billed-cost allocation</div>
    {[["routingSavingsUsd", "Routing savings"], ["promptCacheSavingsUsd", "Provider prompt-cache savings"], ["responseCacheSavingsUsd", "Router response-cache savings"]].map(([key, label]) => typeof summary[key] === "number" && typeof summary.costComponentReportedRequests === "number" && summary.costComponentReportedRequests > 0 && summary.savingsAttributionComplete !== false && (key !== "routingSavingsUsd" || typeof summary.baselineCostUsd === "number" || typeof summary.baseline_cost_usd === "number") ? <Card key={key}><CardContent className="p-4"><div>${(summary[key] as number).toFixed(6)}</div><div>{label}</div></CardContent></Card> : null)}
    {summary.savingsAttributionComplete === false && <div className="col-span-full text-xs text-white/58">Savings attribution is incomplete; total spend remains available, but historical or partial component evidence cannot establish a complete savings split.</div>}
    <div className="col-span-full text-xs text-white/58">Reasoning is a subset of output, and cache reads/writes are subsets of input. Shares show known tokens across all requests; partial coverage is not a complete tokenizer breakdown. Uncached input includes input not attributed to reported caching; output not attributed to reasoning may include undisclosed reasoning.</div>
  </>;
}
