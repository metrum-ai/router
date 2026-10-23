// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { TokenUsageBreakdown, tokenUsageParts, costUsageParts } from "./TokenUsageBreakdown";

describe("token usage breakdown", () => {
 it("does not call reported zero tokens missing when the denominator is zero",()=>{
  const html=renderToStaticMarkup(<TokenUsageBreakdown summary={{requests:1,inputTokens:0,outputTokens:0,reasoningTokens:0,cachedInputTokens:0,reasoningReportedRequests:1,cachedInputReportedRequests:1}}/>);
  expect(html).toContain("No output");expect(html).toContain("No input");expect(html).not.toContain("Not reported");
 });
 it("partitions cache writes and reasoning without double-counting billed cost",()=>{
  const summary={inputTokens:100,outputTokens:40,cachedInputTokens:60,cacheWriteInputTokens:20,reasoningTokens:20,uncachedInputCostUsd:.0002,cachedInputCostUsd:.00012,cacheWriteCostUsd:.00025,visibleOutputCostUsd:.0004,reasoningOutputCostUsd:.0004};
  expect(tokenUsageParts(summary).map(part=>part.value)).toEqual([20,60,20,20,20]);
  expect(costUsageParts(summary).reduce((sum,part)=>sum+part.value,0)).toBeCloseTo(.00137,10);
 });
 it("distinguishes reported zero from historical unknown evidence",()=>{
  const html=renderToStaticMarkup(<TokenUsageBreakdown summary={{requests:3,inputTokens:100,outputTokens:40,reasoningTokens:0,cachedInputTokens:0,reasoningReportedRequests:2,cachedInputReportedRequests:2,costComponentReportedRequests:0,historySource:"finalized_daily_rollups"}}/>);
  expect(html).toContain("0.0%");expect(html).toContain("2 / 3 requests reported");expect(html).toContain("Cost composition unavailable");expect(html).toContain("finalized daily rollups");
 });
 it("keeps reasoning inside output and cache inside input", () => {
  const parts = tokenUsageParts({inputTokens:100,outputTokens:40,reasoningTokens:20,cachedInputTokens:60});
  expect(parts.map(part=>part.value)).toEqual([40,60,0,20,20]);
  expect(parts.reduce((total,part)=>total+part.value,0)).toBe(140);
 });
 it("shows reporting coverage alongside shares", () => {
  const html = renderToStaticMarkup(<TokenUsageBreakdown summary={{requests:3,inputTokens:100,outputTokens:40,reasoningTokens:20,cachedInputTokens:60,reasoningReportedRequests:2,cachedInputReportedRequests:2}} />);
  expect(html).toContain("50.0%");
  expect(html).toContain("60.0%");
  expect(html).toContain("2 / 3 requests reported");
 });
 it("does not turn missing coverage into a reported zero share", () => {
  const html = renderToStaticMarkup(<TokenUsageBreakdown summary={{requests:3,inputTokens:100,outputTokens:40,reasoningTokens:0,cachedInputTokens:0,reasoningReportedRequests:0,cachedInputReportedRequests:0}} />);
  expect(html).toContain("Not reported");
  expect(html).not.toContain("0.0%");
 });
 it("supports the savings API token names without counting subsets twice", () => {
  expect(tokenUsageParts({input_tokens:100,output_tokens:40,cachedInputTokens:60,reasoningTokens:20}).map(part=>part.value)).toEqual([40,60,0,20,20]);
 });
});
