// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, test } from "vitest";
import { supportedSortKeysForTab } from "../components/ReportPanel";
import { filterFields, globalFilterFields, paginationFilterFields, tabFilterFields } from "./filters";
import { columnsForTab, reportMetadataById, resolveSortKey, rowsForTab, tabById, tabSpecs, type ReportColumn, type TabSpec } from "./reports";

function names(fields: ReadonlyArray<readonly [string, string, string]>) {
  return fields.map(([name]) => name);
}

describe("admin report filters", () => {
  test("distinguishes free count-token events in request rows", () => {
    const tab = tabById("requests");
    if (!tab) throw new Error("requests tab missing");
    const columns = columnsForTab(tab, [{ requestKind: "count_tokens", usageSource: "none", inputTokens: 0 }]);
    expect(columns.map(column => column.key)).toContain("requestKind");
  });
  test("exposes endpoint filtering and the endpoint request column", () => {
    const tab = tabById("requests");
    if (!tab) throw new Error("requests tab missing");
    expect(names(globalFilterFields)).toContain("endpoint");
    expect(columnsForTab(tab, [{ endpoint: "embeddings" }]).map(column => column.key)).toContain("endpoint");
  });
  test("keeps the compatibility filter list as global filters followed by tab and pagination filters", () => {
    expect(filterFields).toEqual([...globalFilterFields, ...tabFilterFields, ...paginationFilterFields]);
  });

  test("does not duplicate keys between global and tab filters", () => {
    const globalNames = new Set(names(globalFilterFields));
    const tabNames = new Set(names(tabFilterFields));
    expect([...globalNames].filter((name) => tabNames.has(name))).toEqual([]);
    expect(new Set(names(filterFields)).size).toBe(filterFields.length);
  });

  test("keeps every global filter accepted by the Go report parser", () => {
    const parserSource = readFileSync(resolve(__dirname, "../../../../admin_reports.go"), "utf8");
    const accepted = new Set<string>(["since"]);
    for (const match of parserSource.matchAll(/q\.Get\("([^"]+)"\)/g)) {
      accepted.add(match[1]);
    }
    for (const [name] of globalFilterFields) {
      expect(accepted.has(name), `${name} must be accepted by parseAdminReportFilters`).toBe(true);
    }
  });
});

describe("admin report help metadata", () => {
  test("defines complete help metadata for every report tab", () => {
    for (const tab of tabSpecs) {
      expect(tab.metadata.shortDescription.trim(), tab.id).not.toBe("");
      expect(tab.metadata.purpose.trim(), tab.id).not.toBe("");
      expect(tab.metadata.dataSemantics.trim(), tab.id).not.toBe("");
      expect(tab.metadata.caveats.trim(), tab.id).not.toBe("");
      expect(tab.metadata.emptyState.trim(), tab.id).not.toBe("");
      expect(tab.metadata.docsPath, tab.id).toContain(`#${tab.id}`);
      expect(tab.metadata.commonFilters.length, tab.id).toBeGreaterThan(0);
      expect(tab.metadata.keyColumns.length, tab.id).toBeGreaterThan(0);
      expect(tab.metadata.relatedReports.length, tab.id).toBeGreaterThan(0);
    }
    expect(Object.keys(reportMetadataById).sort()).toEqual(tabSpecs.map((tab) => tab.id).sort());
  });

  test("adds column descriptions to tab schemas", () => {
    for (const tab of tabSpecs) {
      const columns = columnsForTab(
        tab,
        Object.fromEntries((tab.columns || []).map((column) => [column.key, column.key])) ? [Object.fromEntries((tab.columns || []).map((column) => [column.key, column.key]))] : [],
      );
      for (const column of columns) {
        expect(column.description?.trim(), `${tab.id}.${column.key}`).not.toBe("");
      }
    }
  });
});

describe("admin report sort state", () => {
  test("maps backend cost sort aliases to the visible total-cost column", () => {
    const columns: ReportColumn[] = [{ key: "totalCostUsd", label: "Total cost" }];
    const rows = [{ costUsd: 1.23, totalCostUsd: 1.23 }];
    expect(resolveSortKey("costUsd", rows, columns)).toBe("totalCostUsd");
  });

  test("uses savings-specific sort keys for savings breakdown tabs", () => {
    for (const tab of savingsBreakdownTabs()) {
      const keys = supportedSortKeysForTab(tab);
      expect(keys, tab.id).toBeDefined();
      expect(keys?.has("savingsUsd"), tab.id).toBe(true);
      expect(keys?.has("savingsPct"), tab.id).toBe(true);
      expect(keys?.has("baselineCostUsd"), tab.id).toBe(true);
      expect(keys?.has("requests"), tab.id).toBe(true);
      expect(keys?.has("totalTokens"), tab.id).toBe(true);
      expect(keys?.has("avgCostUsd"), tab.id).toBe(true);
      expect(keys?.has("actualCostUsd") || keys?.has("totalCostUsd"), tab.id).toBe(true);
      expect(keys?.has("costUsd") && keys?.has("totalCostUsd"), tab.id).toBe(false);
    }
  });
});

describe("admin savings report columns", () => {
  test("keeps savings breakdown columns deterministic and savings-specific", () => {
    for (const tab of savingsBreakdownTabs()) {
      const rows = rowsForTab(tab, { rows: [savingsBreakdownRow()] });
      const columns = columnsForTab(tab, rows);
      const labels = columns.map((column) => column.label);
      const expected = expectedSavingsLabels(tab);

      expect(labels, tab.id).toEqual(expected);
      expect(new Set(labels).size, tab.id).toBe(labels.length);
      expect(labels, tab.id).toContain("Savings");
      expect(labels, tab.id).toContain("Savings rate");
      expect(labels, tab.id).not.toContain("Cost");
      expect(labels, tab.id).not.toContain("Total cost");
      expect(columns.map((column) => column.key), tab.id).not.toContain("costUsd");
      expect(columns.map((column) => column.key), tab.id).not.toContain("totalCostUsd");
    }
  });

  test("prefers actualCostUsd and falls back to backend actual-cost aliases", () => {
    const tab = requiredTab("savings-by-key");
    const actualRows = rowsForTab(tab, { rows: [savingsBreakdownRow({ actualCostUsd: 1.5, totalCostUsd: 1.2, costUsd: 1.2 })] });
    const totalRows = rowsForTab(tab, { rows: [savingsBreakdownRow({ actualCostUsd: undefined, totalCostUsd: 1.2, costUsd: 1.2 })] });
    const costRows = rowsForTab(tab, { rows: [savingsBreakdownRow({ actualCostUsd: undefined, totalCostUsd: undefined, costUsd: 1.1 })] });

    expect(columnsForTab(tab, actualRows).map((column) => column.key)).toContain("actualCostUsd");
    expect(actualRows[0].actualCostUsd).toBe(1.5);
    expect(totalRows[0].actualCostUsd).toBe(1.2);
    expect(costRows[0].actualCostUsd).toBe(1.1);
  });

  test("uses visible savings columns as CSV headers", () => {
    const tab = requiredTab("savings-by-provider-model");
    const rows = rowsForTab(tab, { rows: [savingsBreakdownRow()] });
    const csvHeaders = columnsForTab(tab, rows).map((column) => column.label);

    expect(csvHeaders).toEqual([
      "Provider/model",
      "Dialect",
      "Requests",
      "Input tokens",
      "Output tokens",
      "Total tokens",
      "Actual cost",
      "Baseline cost",
      "Savings",
      "Savings rate",
      "Avg cost/request",
    ]);
  });
});

function savingsBreakdownTabs(): TabSpec[] {
  return ["savings-by-user", "savings-by-key", "savings-by-group", "savings-by-project", "savings-by-provider-model"].map(requiredTab);
}

function requiredTab(id: string): TabSpec {
  const tab = tabById(id);
  expect(tab, id).toBeDefined();
  return tab as TabSpec;
}

function expectedSavingsLabels(tab: TabSpec): string[] {
  const dimensionLabels: Record<string, string[]> = {
    "savings-by-user": ["User"],
    "savings-by-key": ["Key"],
    "savings-by-group": ["Model group"],
    "savings-by-project": ["Project", "Environment"],
    "savings-by-provider-model": ["Provider/model", "Dialect"],
  };
  return [
    ...dimensionLabels[tab.id],
    "Requests",
    "Input tokens",
    "Output tokens",
    "Total tokens",
    "Actual cost",
    "Baseline cost",
    "Savings",
    "Savings rate",
    "Avg cost/request",
  ];
}

function savingsBreakdownRow(overrides: Record<string, unknown> = {}) {
  return {
    key: "bucket",
    secondaryKey: "secondary",
    requests: 12,
    inputTokens: 1000,
    outputTokens: 250,
    totalTokens: 1250,
    costUsd: 1.23,
    totalCostUsd: 1.23,
    actualCostUsd: 1.23,
    baselineCostUsd: 2.5,
    savingsUsd: 1.27,
    savingsPct: 50.8,
    avgCostUsd: 0.1025,
    ...overrides,
  };
}
