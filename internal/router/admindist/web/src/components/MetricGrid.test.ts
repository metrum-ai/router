// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, test } from "vitest";
import { buildMetricEntries, type MetricGridConfig } from "./MetricGrid";
import { metricGridConfigForTab } from "./ReportPanel";
import { tabById, type TabSpec } from "@/lib/reports";

const savingsMetricConfig: MetricGridConfig = {
  priority: [
    "savingsUsd",
    "savingsPct",
    "actualCostUsd",
    "totalCostUsd",
    "baselineCostUsd",
    "requests",
    "totalTokens",
    "tokens",
    "avgCostUsd",
    "errors",
    "fallbacks",
  ],
  labels: {
    savingsUsd: "Total savings",
    savingsPct: "Savings rate",
    actualCostUsd: "Actual cost",
    totalCostUsd: "Actual cost",
    baselineCostUsd: "Baseline cost",
    avgCostUsd: "Avg cost/request",
  },
  hiddenKeys: ["totalCostUsd"],
  hideZeroKeys: ["errors", "fallbacks"],
  maxItems: 9,
};

describe("MetricGrid", () => {
  test("prioritizes customer-readable savings summary cards", () => {
    const entries = buildMetricEntries(
      {
        requests: 100,
        errors: 2,
        fallbacks: 1,
        avgLatencyMs: 250,
        totalTokens: 12345,
        actualCostUsd: 3.21,
        totalCostUsd: 3.21,
        baselineCostUsd: 9.87,
        savingsUsd: 6.66,
        savingsPct: 67.5,
        avgCostUsd: 0.0321,
      },
      savingsMetricConfig,
    );

    expect(entries.map((entry) => entry.key)).toEqual([
      "savingsUsd",
      "savingsPct",
      "actualCostUsd",
      "baselineCostUsd",
      "requests",
      "totalTokens",
      "avgCostUsd",
      "errors",
      "fallbacks",
    ]);
    expect(entries.map((entry) => entry.label)).toEqual([
      "Total savings",
      "Savings rate",
      "Actual cost",
      "Baseline cost",
      "Requests",
      "Total Tokens",
      "Avg cost/request",
      "Errors",
      "Fallbacks",
    ]);
  });

  test("uses totalCostUsd as the actual cost fallback and hides irrelevant error cards", () => {
    const entries = buildMetricEntries(
      {
        requests: 10,
        errors: 0,
        fallbacks: 0,
        totalTokens: 2000,
        totalCostUsd: 1.23,
        baselineCostUsd: 4.56,
        savingsUsd: 3.33,
        savingsPct: 73.0,
        avgCostUsd: 0.123,
      },
      { ...savingsMetricConfig, hiddenKeys: undefined },
    );

    expect(entries.map((entry) => entry.key)).toEqual([
      "savingsUsd",
      "savingsPct",
      "totalCostUsd",
      "baselineCostUsd",
      "requests",
      "totalTokens",
      "avgCostUsd",
    ]);
    expect(entries.find((entry) => entry.key === "totalCostUsd")?.label).toBe("Actual cost");
    expect(entries.some((entry) => entry.key === "errors")).toBe(false);
    expect(entries.some((entry) => entry.key === "fallbacks")).toBe(false);
  });

  test("applies savings metric config to every savings tab", () => {
    for (const tabID of ["savings", "savings-by-user", "savings-by-key", "savings-by-group", "savings-by-project", "savings-by-provider-model"]) {
      const tab = requiredTab(tabID);
      const config = metricGridConfigForTab(tab, {
        summary: {
          requests: 10,
          totalTokens: 2000,
          actualCostUsd: 1.23,
          totalCostUsd: 1.23,
          baselineCostUsd: 4.56,
          savingsUsd: 3.33,
          savingsPct: 73.0,
        },
      });
      const entries = buildMetricEntries(
        {
          requests: 10,
          totalTokens: 2000,
          actualCostUsd: 1.23,
          totalCostUsd: 1.23,
          baselineCostUsd: 4.56,
          savingsUsd: 3.33,
          savingsPct: 73.0,
        },
        config,
      );

      expect(entries.slice(0, 4).map((entry) => entry.label), tabID).toEqual(["Total savings", "Savings rate", "Actual cost", "Baseline cost"]);
    }
  });

  test("labels snake_case savings summary fields from the main savings API", () => {
    const entries = buildMetricEntries(
      {
        requests: 10,
        total_tokens: 2000,
        actual_cost_usd: 1.23,
        baseline_cost_usd: 4.56,
        savings_usd: 3.33,
        savings_pct: 73.0,
      },
      metricGridConfigForTab(requiredTab("savings"), {
        summary: {
          requests: 10,
          total_tokens: 2000,
          actual_cost_usd: 1.23,
          baseline_cost_usd: 4.56,
          savings_usd: 3.33,
          savings_pct: 73.0,
        },
      }),
    );

    expect(entries.slice(0, 4).map((entry) => entry.label)).toEqual(["Total savings", "Savings rate", "Actual cost", "Baseline cost"]);
  });
});

function requiredTab(id: string): TabSpec {
  const tab = tabById(id);
  expect(tab, id).toBeDefined();
  return tab as TabSpec;
}
