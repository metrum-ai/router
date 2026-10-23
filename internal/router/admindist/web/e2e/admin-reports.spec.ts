// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import { expect, test, type Page, type Route } from "@playwright/test";
import { readFileSync } from "node:fs";
import { navGroups, validateNavGroups } from "../src/lib/navGroups";
import { globalFilterFields, tabFilterFields, tabSpecs, validateFilterModel, type ReportChart, type ReportRow } from "../src/lib/reports";

const generatedUtc = "2026-06-28T12:00:00Z";
let apiRequests: string[] = [];
const migrationRows = [
  { scope: "usage", migrationId: 2026080501, name: "historical validation", release: "2026.8", state: "pending", dataJobKey: "historical-usage-validation-v1", dataJobState: "missing", schemaVersion: 2, dataVersion: 1, maintenanceMode: "online", executionMode: "transactional", lockClass: "online", timeoutClass: "bounded", rollbackClass: "restore-required", validationState: "not-validated", postcondition: "usage.historical-validation.schema.v1", checkpoints: 0, rowsScanned: 0, rowsUpdated: 0, rowsSkipped: 0, rowsFailed: 0, startedAt: generatedUtc },
  { scope: "usage", migrationId: 2026080502, name: "running validation", release: "2026.8", state: "in-progress", dataJobKey: "running-validation-v1", dataJobState: "running", schemaVersion: 2, dataVersion: 1, maintenanceMode: "online", executionMode: "transactional", lockClass: "online", timeoutClass: "bounded", rollbackClass: "restore-required", validationState: "in-progress", postcondition: "usage.running-validation.schema.v1", checkpoints: 2, rowsScanned: 20, rowsUpdated: 10, rowsSkipped: 8, rowsFailed: 2, startedAt: generatedUtc },
  { scope: "usage", migrationId: 2026080507, name: "failed schema ledger", release: "2026.8", state: "failed", dataJobKey: "conflicting-validated-job-v1", dataJobState: "validated", schemaVersion: 2, dataVersion: 1, maintenanceMode: "online", executionMode: "transactional", lockClass: "online", timeoutClass: "bounded", rollbackClass: "restore-required", errorClass: "schema-failed", errorMessage: "schema failed", validationState: "failed", postcondition: "usage.failed-schema-ledger.v1", checkpoints: 3, rowsScanned: 30, rowsUpdated: 15, rowsSkipped: 15, rowsFailed: 0, startedAt: generatedUtc, completedAt: generatedUtc },
  { scope: "usage", migrationId: 2026080503, name: "failed validation", release: "2026.8", state: "failed", dataJobKey: "failed-validation-v1", dataJobState: "failed", schemaVersion: 2, dataVersion: 1, maintenanceMode: "online", executionMode: "transactional", lockClass: "online", timeoutClass: "bounded", rollbackClass: "restore-required", errorClass: "data-job-failed", errorMessage: "migration safe failure", validationState: "failed", postcondition: "usage.failed-validation.schema.v1", checkpoints: 2, rowsScanned: 20, rowsUpdated: 10, rowsSkipped: 8, rowsFailed: 2, startedAt: generatedUtc, completedAt: generatedUtc },
  { scope: "usage", migrationId: 2026080504, name: "paused validation", release: "2026.8", state: "pending", dataJobKey: "paused-validation-v1", dataJobState: "paused", schemaVersion: 2, dataVersion: 1, maintenanceMode: "online", executionMode: "transactional", lockClass: "online", timeoutClass: "bounded", rollbackClass: "restore-required", validationState: "not-validated", postcondition: "usage.paused-validation.schema.v1", checkpoints: 1, rowsScanned: 10, rowsUpdated: 5, rowsSkipped: 5, rowsFailed: 0, startedAt: generatedUtc },
  { scope: "usage", migrationId: 2026080505, name: "cancelled validation", release: "2026.8", state: "pending", dataJobKey: "cancelled-validation-v1", dataJobState: "cancelled", schemaVersion: 2, dataVersion: 1, maintenanceMode: "online", executionMode: "transactional", lockClass: "online", timeoutClass: "bounded", rollbackClass: "restore-required", validationState: "not-validated", postcondition: "usage.cancelled-validation.schema.v1", checkpoints: 1, rowsScanned: 10, rowsUpdated: 5, rowsSkipped: 5, rowsFailed: 0, startedAt: generatedUtc },
  { scope: "usage", migrationId: 2026080506, name: "validated historical validation", release: "2026.8", state: "applied", dataJobKey: "validated-validation-v1", dataJobState: "validated", schemaVersion: 2, dataVersion: 1, maintenanceMode: "online", executionMode: "transactional", lockClass: "online", timeoutClass: "bounded", rollbackClass: "restore-required", validationState: "verified", postcondition: "usage.validated-validation.schema.v1", checkpoints: 3, rowsScanned: 30, rowsUpdated: 15, rowsSkipped: 15, rowsFailed: 0, startedAt: generatedUtc, completedAt: generatedUtc },
  { scope: "usage", migrationId: 2026080508, name: "running schema ledger", release: "2026.8", state: "running", dataJobKey: "conflicting-failed-job-v1", dataJobState: "failed", schemaVersion: 2, dataVersion: 1, maintenanceMode: "online", executionMode: "transactional", lockClass: "online", timeoutClass: "bounded", rollbackClass: "restore-required", errorClass: "schema-running", errorMessage: "schema running", validationState: "in-progress", postcondition: "usage.running-schema-ledger.v1", checkpoints: 2, rowsScanned: 20, rowsUpdated: 10, rowsSkipped: 8, rowsFailed: 2, startedAt: generatedUtc },
];

test.beforeEach(async ({ page }) => {
  apiRequests = [];
  await installAdminApiMocks(page, apiRequests);
});

test("overview distinguishes estimated usage and preserves the source filter", async ({ page }) => {
  await page.route("**/api/summary*", (route) => route.fulfill({ json: {
    summary: { requests: 4, totalTokens: 200, estimatedTokenShare: 25, estimatedInputTokens: 10, estimatedOutputTokens: 40 },
    charts: [], requests: [],
  } }));
  await page.goto("/?tab=overview&usage_source=estimated");
  await expect(page.getByText("Estimated usage", { exact: true })).toBeVisible();
  await expect(page.getByText("25.0%", { exact: true })).toBeVisible();
  await page.screenshot({ path: "test-results/usage-provenance-overview.png", fullPage: true });
});

test("count-token control events remain identifiable in the request table", async ({ page }) => {
  await page.route("**/api/requests?*", route => route.fulfill({ json: commonResponse("requests", {
    requests: [{ ...requestRow("req_count_unbilled"), requestKind: "count_tokens", usageSource: "none", inputTokens: 0, outputTokens: 0, totalTokens: 0, costUsd: 0, totalCostUsd: 0 }],
  }, cursorPagination(new URL(route.request().url()), 1, 1, false)) }));
  await page.goto("/?tab=requests");
  await expect(page.getByRole("columnheader", { name: "Request kind" })).toBeVisible();
  await expect(page.getByRole("cell", { name: "count_tokens", exact: true })).toBeVisible();
  await page.screenshot({ path: "test-results/count-tokens-request-kind.png", fullPage: true });
});

function globalFiltersButton(page: Page) {
  return page.getByRole("button", { name: "Filters", exact: true }).first();
}

async function boundingBox(locator: ReturnType<Page["locator"]>) {
  const box = await locator.boundingBox();
  expect(box).not.toBeNull();
  return box!;
}

test("admin report nav groups cover every tab exactly once", async () => {
  const result = validateNavGroups(navGroups, tabSpecs);
  expect(result.duplicateTabs).toEqual([]);
  expect(result.missingTabs).toEqual([]);
  expect(result.unknownTabs).toEqual([]);
  expect(result.uniqueGroupIds).toBe(true);
  expect(result.nonEmptyLabels).toBe(true);
  expect(navGroups.map((group) => group.id)).toEqual([
    "overview",
    "usage",
    "savings",
    "performance",
    "traffic-shaping",
    "routing-decisions",
    "provider-catalog",
    "security",
    "request-drilldown",
    "system-status",
    "operations",
  ]);
});

test("data migrations is a read-only operational report", async ({ page }) => {
  await page.goto("/?tab=data-migrations&scope=usage&release=2026.8&state=failed&type=transactional&date=2026-08");
  await expect(page.getByRole("heading", { name: "Data migrations", exact: true })).toBeVisible();
  await expect(page.getByText(/read-only/i).first()).toBeVisible();
  for (const filter of ["scope", "release", "state", "type", "date"]) await expect(page.locator(`[data-tab-filter="${filter}"]`)).toBeVisible();
  await page.locator("[data-migration-detail-panel] summary").click();
  await expect(page.locator("[data-migration-detail-panel]")).toContainText("schema failed");
  await expect(page.locator("[data-migration-detail-panel]")).toContainText("usage.failed-schema-ledger.v1");
  await expect(page.locator("[data-migration-detail-panel]")).toContainText("3 checkpoints; 30 scanned");
  await expect(page.locator("[data-migration-detail-panel]")).not.toContainText("postgres://");
  expect(apiRequests.some((request) => request.includes("api/migrations"))).toBe(true);
  expect(apiRequests.some((request) => request.includes("scope=usage") && request.includes("state=failed"))).toBe(true);
  await expect(page.getByText(/apply|retry|restore/i).first()).toBeVisible();
});

test("data migrations shows effective data-job state without certifying unfinished work", async ({ page }) => {
  for (const [state, jobState, validation] of [["pending", "missing", "not-validated"], ["in-progress", "running", "in-progress"], ["failed", "failed", "failed"], ["applied", "validated", "verified"]]) {
    await page.goto(`/?tab=data-migrations&state=${state}`);
    await expect(page.getByRole("heading", { name: "Data migrations", exact: true })).toBeVisible();
    await expect(page.getByText("Effective state", { exact: true }).first()).toBeVisible();
    await expect(page.locator("tbody").first()).toContainText(jobState);
    await expect(page.locator("tbody").first()).toContainText(validation);
    await page.locator("[data-migration-detail-panel] summary").click();
    await expect(page.locator("[data-migration-detail-panel]")).toContainText("Effective state");
    await expect(page.locator("[data-migration-detail-panel]")).toContainText("Data-job state");
  }
  await page.goto("/?tab=data-migrations&state=pending");
  await expect(page.locator("tbody").first()).toContainText("paused");
  await expect(page.locator("tbody").first()).toContainText("cancelled");
});

test("data migrations keeps non-applied schema ledger state ahead of conflicting job state", async ({ page }) => {
  for (const [state, jobState, validation, name] of [["failed", "validated", "failed", "failed schema ledger"], ["running", "failed", "in-progress", "running schema ledger"]]) {
    await page.goto(`/?tab=data-migrations&state=${state}`);
    await expect(page.getByRole("heading", { name: "Data migrations", exact: true })).toBeVisible();
    const row = page.getByRole("row").filter({ hasText: name });
    await expect(row).toContainText(jobState);
    await expect(row).toContainText(validation);
    await page.locator("[data-migration-detail-panel] summary").click();
    await expect(page.locator("[data-migration-detail-panel]")).toContainText(state);
    await expect(page.locator("[data-migration-detail-panel]")).toContainText(jobState);
  }
});

test("admin report filter model splits global and tab filters without overlap", async () => {
  const result = validateFilterModel();
  expect(result.duplicateNames).toEqual([]);
  expect(result.overlappingNames).toEqual([]);
  expect(result.hasEveryGlobal).toBe(true);
  expect(result.hasEveryTab).toBe(true);
  expect(globalFilterFields.map(([name]) => name)).toContain("since");
  expect(tabFilterFields.map(([name]) => name)).toEqual([
    "baseline",
    "status",
    "cache",
    "traffic_shape_bucket",
    "traffic_shape_scope",
    "sort",
    "direction",
    "limit",
    "scope",
    "release",
    "state",
    "type",
    "date",
  ]);
});

test("admin reports shell renders every tab with mocked report APIs", async ({ page }) => {
  const consoleErrors: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error") consoleErrors.push(message.text());
  });

  await page.goto("/");

  await expect(page.getByRole("heading", { name: "Admin Reports" })).toBeVisible();
  await expect(page.getByText("v2026.e2e")).toBeVisible();
  await expect(page.getByText("Commit e2ecommit123")).toBeVisible();

  const reportNav = page.getByRole("navigation", { name: "Report sections" });
  await expect(reportNav.locator("[data-report-tab]")).toHaveCount(tabSpecs.length);
  const horizontalOverflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
  expect(horizontalOverflow).toBe(false);

  for (const spec of tabSpecs) {
    await reportNav.locator(`[data-report-tab="${spec.id}"]`).click();
    await expect(page.getByRole("heading", { name: spec.label, exact: true })).toBeVisible();
    await expect(page.locator("main canvas, main table").first()).toBeVisible();
  }

  expect(consoleErrors).toEqual([]);
});

test("category charts use short axis labels with a collapsible bucket legend", async ({ page }) => {
  await page.goto("/?tab=provider-model-mix&since=24h&limit=50");

  await expect(page.getByRole("heading", { name: "Provider/model", exact: true })).toBeVisible();
  const legend = page.locator("[data-chart-bucket-legend]").first();
  await expect(legend.getByRole("button", { name: /Show bucket legend \(10\)/ })).toBeVisible();
  await expect(legend.locator("li")).toHaveCount(0);

  await legend.getByRole("button", { name: /Show bucket legend \(10\)/ }).click();
  await expect(legend.getByRole("button", { name: /Hide bucket legend \(10\)/ })).toHaveAttribute("aria-expanded", "true");
  await expect(legend.locator("li")).toHaveCount(10);
  await expect(legend.getByText("deepseek-v4-fla…")).toBeVisible();
  await expect(legend.getByText("fireworks/accounts/fireworks/models/deepseek-v4-flash")).toBeVisible();

  const horizontalOverflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
  expect(horizontalOverflow).toBe(false);
});

test("category chart hover shows full-label tooltip and dismisses", async ({ page }) => {
  await page.goto("/?tab=provider-model-mix&since=24h&limit=50");

  const chart = page.locator('[data-chart-id="provider-model-mix-requests"]').first();
  const interactionLayer = chart.locator("[data-chart-hover-layer]").first();
  const box = await boundingBox(interactionLayer);
  expect(box.width).toBeGreaterThan(0);
  expect(box.height).toBeGreaterThan(0);
  await interactionLayer.hover({ position: { x: box.width * 0.12, y: box.height * 0.5 } });
  await interactionLayer.dispatchEvent("mousemove", {
    clientX: box.x + box.width * 0.12,
    clientY: box.y + box.height * 0.5,
  });

  const tooltip = page.locator("[data-admin-chart-tooltip]");
  await expect(tooltip).toBeVisible();
  await expect(tooltip).toContainText("fireworks/accounts/fireworks/models/");
  await expect(tooltip).toContainText("Requests:");

  await interactionLayer.dispatchEvent("mousemove", {
    clientX: box.x + box.width * 0.96,
    clientY: box.y + box.height * 0.5,
  });
  await expect(tooltip).toBeVisible();
  const tooltipBox = await boundingBox(tooltip);
  const viewport = page.viewportSize();
  expect(viewport).not.toBeNull();
  expect(tooltipBox.x).toBeGreaterThanOrEqual(0);
  expect(tooltipBox.x + tooltipBox.width).toBeLessThanOrEqual(viewport!.width);

  await page.keyboard.press("Escape");
  await expect(tooltip).toBeHidden();
});

test("sidebar groups collapse and expand on click", async ({ page }) => {
  await page.goto("/");

  const reportNav = page.getByRole("navigation", { name: "Report sections" });
  for (const group of navGroups) {
    await expect(reportNav.locator(`[data-nav-group="${group.id}"] > button`).first()).toBeVisible();
  }

  await reportNav.getByRole("button", { name: "Usage", exact: true }).click();
  await expect(reportNav.getByRole("button", { name: "Groups", exact: true })).toBeHidden();

  await reportNav.getByRole("button", { name: "Usage", exact: true }).click();
  await expect(reportNav.getByRole("button", { name: "Groups", exact: true })).toBeVisible();

  await reportNav.getByRole("button", { name: "Usage", exact: true }).click();
  await page.reload();
  const reloadedNav = page.getByRole("navigation", { name: "Report sections" });
  await expect(reloadedNav.getByRole("button", { name: "Groups", exact: true })).toBeHidden();
});

test("sidebar keyboard navigation reaches every group", async ({ page }) => {
  await page.goto("/");

  const reportNav = page.getByRole("navigation", { name: "Report sections" });
  await reportNav.locator('[data-nav-group="overview"] button').first().focus();

  const reachedGroups = new Set<string>();
  for (let index = 0; index < tabSpecs.length + navGroups.length + 8; index += 1) {
    const groupId = await page.evaluate(() => {
      const focused = document.activeElement;
      if (!(focused instanceof HTMLElement) || !focused.hasAttribute("data-report-tab")) return "";
      return focused.closest("[data-nav-group]")?.getAttribute("data-nav-group") || "";
    });
    if (groupId) reachedGroups.add(groupId);
    await page.keyboard.press("Tab");
  }

  expect(Array.from(reachedGroups).sort()).toEqual(navGroups.map((group) => group.id).sort());
});

test("mobile viewport shows drawer toggle and hidden sidebar", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 800 });
  await page.goto("/");

  await expect(page.locator("[data-report-sidebar]:visible")).toHaveCount(0);
  await page.getByRole("button", { name: "Open report navigation" }).click();

  const reportNav = page.getByRole("navigation", { name: "Report sections" });
  await expect(reportNav).toBeVisible();
  await expect(reportNav.locator("[data-report-tab]")).toHaveCount(tabSpecs.length);
  await reportNav.getByRole("button", { name: "Requests", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Requests", exact: true })).toBeVisible();
  await expect(page.locator("[data-report-sidebar]:visible")).toHaveCount(0);
});

test("header uses two stacked rows on desktop", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto("/");

  const header = page.locator("header");
  const brandRow = header.locator("[data-admin-header-brand]");
  const heading = header.getByRole("heading", { name: "Admin Reports" });
  const filtersButton = globalFiltersButton(page);
  await expect(brandRow).toBeVisible();
  await expect(filtersButton).toBeVisible();

  const headerBox = await boundingBox(header);
  const brandBox = await boundingBox(brandRow);
  const headingBox = await boundingBox(heading);
  const filtersBox = await boundingBox(filtersButton);

  expect(headerBox.height).toBeGreaterThan(brandBox.height + 40);
  expect(headingBox.y).toBeLessThan(filtersBox.y);
  expect(Math.abs(filtersBox.x - brandBox.x)).toBeLessThan(4);
});

test("header filter action row uses full available width", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto("/");

  const header = page.locator("header");
  const headerBox = await boundingBox(header);
  const filtersBox = await boundingBox(globalFiltersButton(page));
  const applyBox = await boundingBox(page.getByRole("button", { name: "Apply" }));

  expect(filtersBox.x - headerBox.x).toBeLessThan(100);
  expect(applyBox.x - headerBox.x).toBeGreaterThan(600);

  const horizontalOverflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
  expect(horizontalOverflow).toBe(false);
});

test("deep link requests tab opens active sidebar item", async ({ page }) => {
  await page.goto("/?tab=requests");

  const reportNav = page.getByRole("navigation", { name: "Report sections" });
  const requestsTab = reportNav.getByRole("button", { name: "Requests", exact: true });
  await expect(requestsTab).toHaveAttribute("aria-current", "page");
  await expect(page.getByRole("heading", { name: "Requests", exact: true })).toBeVisible();
});

test("report help related links navigate to another report", async ({ page }) => {
  await page.goto("/?tab=errors-fallbacks&since=24h&cursor=stale");

  await page.getByText("How to use this report").click();
  await page.locator("main").getByRole("link", { name: "Requests", exact: true }).click();

  await expect(page).toHaveURL(/tab=requests/);
  await expect(page).not.toHaveURL(/cursor=stale/);
  await expect(page.getByRole("heading", { name: "Requests", exact: true })).toBeVisible();
});

test("filter URL state and CSV export remain usable", async ({ page }) => {
  await page.goto("/");

  await globalFiltersButton(page).click();
  await page.getByLabel("Caller").fill("alice");
  await page.getByLabel("IP").fill("203.0.113.10");
  await page.getByLabel("Client").fill("codex-cli");
  await page.getByRole("button", { name: "Apply" }).click();

  await expect(page).toHaveURL(/caller_id=alice/);
  await expect(page).toHaveURL(/caller_ip=203\.0\.113\.10/);
  await expect(page).toHaveURL(/client=codex-cli/);

  await page.getByPlaceholder("Filter returned top-N rows").fill("mock");
  await expect(page.locator("tbody tr")).toHaveCount(1);

  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "CSV top-N rows" }).click();
  await expect((await download).suggestedFilename()).toBe("admin-report.csv");
});

test("header global filters render and survive tab switch", async ({ page }) => {
  await page.goto("/?caller_user=alice&provider=mock&since=6d");

  await expect(page.getByLabel("Since")).toHaveValue("6d");
  await globalFiltersButton(page).click();
  await expect(page.getByLabel("User")).toHaveValue("alice");
  await expect(page.getByLabel("Provider")).toHaveValue("mock");

  const reportNav = page.getByRole("navigation", { name: "Report sections" });
  await reportNav.getByRole("button", { name: "Providers", exact: true }).click();
  await expect(page).toHaveURL(/tab=providers/);
  await expect(page).toHaveURL(/caller_user=alice/);
  await expect(page).toHaveURL(/provider=mock/);
  await expect(page).toHaveURL(/since=6d/);
  await expect(page.getByLabel("User")).toHaveValue("alice");
  await expect(page.getByRole("textbox", { name: "Provider" })).toHaveValue("mock");
});

test("header disclosure hides tab-specific inputs and persists state", async ({ page }) => {
  await page.goto("/");

  const filtersButton = globalFiltersButton(page);
  await expect(filtersButton).toHaveAttribute("aria-expanded", "false");
  for (const label of ["Status", "Baseline", "Sort", "Direction", "Shape bucket", "Shape scope", "Rows"]) {
    await expect(page.locator("header").getByLabel(label)).toHaveCount(0);
  }

  await filtersButton.click();
  await expect(filtersButton).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByLabel("Provider")).toBeVisible();
  for (const label of ["Status", "Baseline", "Sort", "Direction", "Shape bucket", "Shape scope", "Rows"]) {
    await expect(page.locator("header").getByLabel(label)).toHaveCount(0);
  }

  await page.reload();
  await expect(globalFiltersButton(page)).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByLabel("Provider")).toBeVisible();

  await globalFiltersButton(page).click();
  await page.reload();
  await expect(globalFiltersButton(page)).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByLabel("Provider")).toBeHidden();
});

test("header Apply still commits all global filter changes to URL", async ({ page }) => {
  await page.goto("/");

  await globalFiltersButton(page).click();
  await page.getByLabel("Caller").fill("alice");
  await page.getByLabel("IP").fill("203.0.113.10");
  await page.getByRole("button", { name: "Apply" }).click();
  await expect(page).toHaveURL(/caller_id=alice/);
  await expect(page).toHaveURL(/caller_ip=203\.0\.113\.10/);

  await page.getByLabel("Provider").fill("mock");
  await globalFiltersButton(page).click();
  await page.getByRole("button", { name: "Apply" }).click();
  await expect(page).toHaveURL(/provider=mock/);
});

test("deep link populates global inputs", async ({ page }) => {
  await page.goto("/?caller_id=alice&caller_user=alice%40example.com&provider=mock&since=6d&dialect=openai");

  await expect(page.getByLabel("Since")).toHaveValue("6d");
  await globalFiltersButton(page).click();
  await expect(page.getByLabel("Caller")).toHaveValue("alice");
  await expect(page.getByLabel("User")).toHaveValue("alice@example.com");
  await expect(page.getByLabel("Provider")).toHaveValue("mock");
  await expect(page.getByLabel("Dialect")).toHaveValue("openai");
});

test("Markdown export uses combined global and tab filters", async ({ page }) => {
  await page.goto("/?tab=savings-by-key&baseline=gpt-5.5&caller_user=alice");

  const href = await page.getByRole("link", { name: "Markdown full report for current filters" }).getAttribute("href");
  expect(href).toContain("export.md?");
  expect(href).toContain("baseline=gpt-5.5");
  expect(href).toContain("caller_user=alice");
});

test("active hidden tab filters are surfaced and can be cleared", async ({ page }) => {
  await page.goto("/?tab=savings-by-key&baseline=gpt-5.5&status=500&caller_user=alice");

  await expect(page.getByRole("button", { name: "Clear tab filters (2)" })).toBeVisible();
  await page.getByRole("button", { name: "Clear tab filters (2)" }).click();

  await expect(page).not.toHaveURL(/baseline=/);
  await expect(page).not.toHaveURL(/status=/);
  await expect(page).toHaveURL(/caller_user=alice/);
  await expect(page.getByRole("button", { name: /Clear tab filters/ })).toHaveCount(0);
});

test("per-tab filter panel shows only relevant inputs for savings tabs", async ({ page }) => {
  await page.goto("/?tab=savings-by-key");

  await expect(page.locator('[data-tab-filter="baseline"]')).toBeVisible();
  await expect(page.locator('[data-tab-filter="sort"]')).toBeVisible();
  await expect(page.locator('[data-tab-filter="direction"]')).toBeVisible();
  await expect(page.locator('[data-tab-filter="status"]')).toHaveCount(0);
  await expect(page.locator('[data-tab-filter="cache"]')).toHaveCount(0);
  await expect(page.locator('[data-tab-filter="traffic_shape_bucket"]')).toHaveCount(0);
});

test("per-tab filter panel shows shaping filters only on shaping tabs", async ({ page }) => {
  await page.goto("/?tab=traffic-shaping-by-user");

  await expect(page.locator('[data-tab-filter="traffic_shape_bucket"]')).toBeVisible();
  await expect(page.locator('[data-tab-filter="traffic_shape_scope"]')).toBeVisible();

  await page.getByRole("navigation", { name: "Report sections" }).getByRole("button", { name: "Errors", exact: true }).click();
  await expect(page.locator('[data-tab-filter="traffic_shape_bucket"]')).toHaveCount(0);
  await expect(page.locator('[data-tab-filter="traffic_shape_scope"]')).toHaveCount(0);
  await expect(page.locator('[data-tab-filter="status"]')).toBeVisible();
  await expect(page.locator('[data-tab-filter="cache"]')).toBeVisible();
});

test("Rows select in DataTable toolbar updates limit and URL", async ({ page }) => {
  await page.goto("/?tab=expensive-requests");

  await page.getByLabel("Rows", { exact: true }).selectOption("100");
  await expect(page).toHaveURL(/limit=100/);
  await globalFiltersButton(page).click();
  await page.getByRole("textbox", { name: "Caller" }).fill("alice");
  await page.getByRole("button", { name: "Apply" }).click();
  await expect(page).toHaveURL(/caller_id=alice/);
  await expect(page).toHaveURL(/limit=100/);
  await page.reload();
  await expect(page.getByLabel("Rows", { exact: true })).toHaveValue("100");
});

test("Reset filters link clears only per-tab filters", async ({ page }) => {
  await page.goto("/?caller_user=alice&tab=savings-by-key&baseline=gpt-5.5&sort=savingsUsd");

  await page.getByRole("button", { name: "Reset filters" }).click();
  await expect(page).not.toHaveURL(/baseline=/);
  await expect(page).not.toHaveURL(/sort=/);
  await expect(page).toHaveURL(/caller_user=alice/);
});

test("deep-linked savings URLs preserve baseline and sorting params", async ({ page }) => {
  await page.goto("/?tab=savings-by-key&since=6d&limit=50&baseline=gpt-5.5&sort=savingsUsd&direction=desc");

  await expect(page.getByRole("heading", { name: "Savings by key", exact: true })).toBeVisible();
  await expect(page.locator('[data-tab-filter="baseline"]')).toHaveValue("gpt-5.5");
  await expect(page.locator('[data-tab-filter="sort"]')).toHaveValue("savingsUsd");
  await expect(page.locator('[data-tab-filter="direction"]')).toHaveValue("desc");
  await expect(page.getByRole("columnheader", { name: "Key" })).toBeVisible();
  await expect(page.getByRole("columnheader", { name: "Actual cost(USD)" })).toBeVisible();
  await expect(page.getByRole("columnheader", { name: "Baseline cost(USD)" })).toBeVisible();
  await expect(page.getByRole("columnheader", { name: "Savings(USD)" })).toBeVisible();
  await expect(page.getByRole("columnheader", { name: "Savings rate(%)" })).toBeVisible();
  await expect(page.getByRole("columnheader", { name: "Avg upstream(ms)" })).toHaveCount(0);
  await expect(page.locator("tbody tr").first()).toContainText("rtr_mock_public");
  await expect(page.getByRole("heading", { name: "Actual vs baseline cost" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Savings", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Savings rate" })).toBeVisible();
  await expect(page.locator("main canvas")).toHaveCount(4);

  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "CSV top-N rows" }).click();
  const path = await (await download).path();
  expect(path).toBeTruthy();
  const csv = readFileSync(path!, "utf8");
  expect(csv.split("\n")[0]).toContain('"Key","Requests","Input tokens","Output tokens","Total tokens","Actual cost","Baseline cost","Savings","Savings rate"');

  const savingsRequest = apiRequests.find((url) => url.includes("/api/savings-by-key?"));
  expect(savingsRequest).toContain("baseline=gpt-5.5");
  expect(savingsRequest).toContain("sort=savingsUsd");
  expect(savingsRequest).toContain("direction=desc");
});

test("cursor-paged request table navigates with URL cursors", async ({ page }) => {
  await page.goto("/?tab=requests&limit=2");

  await expect(page.getByText("Showing 1-2 of 3.")).toBeVisible();
  await page.getByRole("button", { name: "Next page" }).click();
  await expect(page).toHaveURL(/cursor=page-2/);
  await expect(page.getByText("Showing 3-3 of 3.")).toBeVisible();
  expect(apiRequests.some((url) => url.includes("/api/requests?") && url.includes("cursor=page-2"))).toBe(true);

  await page.getByRole("button", { name: "Previous page" }).click();
  await expect(page).not.toHaveURL(/cursor=/);
  await expect(page.getByText("Showing 1-2 of 3.")).toBeVisible();
});

test("request table sort and filters reset cursor state", async ({ page }) => {
  await page.goto("/?tab=requests&limit=2");

  await page.getByRole("button", { name: "Next page" }).click();
  await expect(page).toHaveURL(/cursor=page-2/);

  await page.getByRole("button", { name: /Total cost/ }).click();
  await expect(page).toHaveURL(/sort=totalCostUsd/);
  await expect(page).toHaveURL(/direction=desc/);
  await expect(page).not.toHaveURL(/cursor=/);

  await page.getByRole("button", { name: "Next page" }).click();
  await expect(page).toHaveURL(/cursor=page-2/);
  await globalFiltersButton(page).click();
  await page.getByRole("textbox", { name: "Caller" }).fill("alice");
  await page.getByRole("button", { name: "Apply" }).click();
  await expect(page).toHaveURL(/caller_id=alice/);
  await expect(page).not.toHaveURL(/cursor=/);
});

test("top-N aggregate tables do not show cursor pagination controls", async ({ page }) => {
  await page.goto("/?tab=provider-model-mix&limit=50");

  await expect(page.getByText("Showing top 1 rows, more available.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Next page" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "CSV top-N rows" })).toBeVisible();
});

test("expired cursor errors offer a first-page reset", async ({ page }) => {
  await page.goto("/?tab=requests&limit=2&cursor=expired");

  await expect(page.getByText("requests failed: invalid cursor")).toBeVisible();
  await page.getByRole("button", { name: "Reset to first page" }).click();
  await expect(page).not.toHaveURL(/cursor=/);
  await expect(page.getByText("Showing 1-2 of 3.")).toBeVisible();
});

async function installAdminApiMocks(page: Page, requestedUrls?: string[]) {
  await page.route("**/api/**", async (route) => {
    requestedUrls?.push(route.request().url());
    const response = responseForRoute(route);
    if ("status" in response && "body" in response) {
      await route.fulfill({
        status: response.status,
        contentType: "application/json",
        body: JSON.stringify(response.body),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(response),
    });
  });
}

function responseForRoute(route: Route) {
  const url = new URL(route.request().url());
  const endpoint = url.pathname.split("/api/")[1] || "";
  if (endpoint === "version") {
    return {
      version: "2026.e2e",
      commit: "e2ecommit123456",
      build_date: generatedUtc,
      go_version: "go1.26.0",
      goos: "linux",
      goarch: "amd64",
      license_compile_mode: "release",
    };
  }
  if (endpoint === "provider-catalog-status") {
    return commonResponse("provider-catalog-status", {
      rows: [
        {
          source: "active_target",
          provider: "mock",
          model: "mock-model",
          dialect: "openai",
          activeGroups: ["default"],
          activeTargetCount: 1,
          validationStatus: "passed",
          forceStoreFalse: true,
          outputTokenField: "max_completion_tokens",
          pricingMissing: false,
        },
      ],
    });
  }
  if (endpoint === "retention-status") {
    return {
      generatedUtc,
      enabled: true,
      dryRun: true,
      tables: [{ dataClass: "usage_detail", tableName: "request_usage", status: "dry_run", candidateRows: 2, eligibleRows: 2, heldRows: 0, blockedRows: 0, deletedRows: 0 }],
      rollups: [{ rollupType: "daily", status: "finalized", sourceRequestCount: 2, dailyRows: 1, rollupRows: 1, windowStart: "2026-06-27T00:00:00Z", windowEnd: "2026-06-28T00:00:00Z" }],
      charts: [chart("retention-status")],
    };
  }
  if (endpoint === "migrations") {
    const rows = migrationRows.filter((row) => !url.searchParams.get("state") || row.state === url.searchParams.get("state"));
    return commonResponse(endpoint, { summary: { scope: "usage", schemaVersion: 2, dataVersion: 1, compatible: false, state: url.searchParams.get("state") || "failed", pending: 3, inProgress: 1, failed: 1, verified: 1, missingDataJobs: 1, jobs: 5 }, rows });
  }
  if (endpoint === "summary") {
    return commonResponse("summary", {
      byGroup: [row("default")],
      byProvider: [row("mock/mock-model")],
      byToken: [row("rtr_mock_public")],
      byStatus: [row("200")],
      requests: [requestRow()],
    });
  }
  if (endpoint === "provider-model-mix") {
    return commonResponse(endpoint, {
      charts: [longProviderModelChart()],
      rows: [row(endpoint)],
    }, topNPagination(1, true));
  }
  if (endpoint === "security/events") {
    if (url.searchParams.get("cursor") === "expired") return invalidCursorResponse();
    return commonResponse("security/events", {
      rows: [
        {
          timeUtc: generatedUtc,
          eventType: "admin_report",
          surface: "admin_reports",
          method: "GET",
          path: "/admin/reports/api/summary",
          status: 200,
          outcome: "allowed",
          reason: "authorized",
          authSubject: "basic:admin",
          authSource: "basic",
        },
      ],
    }, cursorPagination(url, 1, 1, false));
  }
  if (endpoint === "savings") {
    return {
      ...commonResponse("savings", {
        byTime: [savingsRow("2026-06-28T12:00:00Z")],
        byGroup: [savingsRow("default")],
      }),
      baseline: {
        baseline_id: "gpt-5.5",
        baseline_name: "GPT-5.5",
        pricing_source: "https://example.invalid/pricing",
        pricing_updated_at: "2026-06-25",
      },
      baselines: [],
    };
  }
  if (endpoint === "savings-by-key") {
    return commonResponse(endpoint, {
      baseline: {
        baseline_id: "gpt-5.5",
        baseline_name: "GPT-5.5",
      },
      charts: savingsBreakdownCharts(endpoint),
      rows: [
        {
          ...row("rtr_mock_public"),
          baselineCostUsd: 0.25,
          savingsUsd: 0.2,
          savingsPct: 80,
        },
      ],
    });
  }
  if (endpoint === "expensive-requests") {
    return commonResponse(endpoint, { requests: [requestRow()] }, cursorPagination(url, 1, 1, false, "costUsd"));
  }
  if (endpoint === "requests") {
    if (url.searchParams.get("cursor") === "expired") return invalidCursorResponse();
    const secondPage = url.searchParams.get("cursor") === "page-2";
    return commonResponse(endpoint, {
      requests: secondPage ? [requestRow("req_e2e_345", 345)] : [requestRow("req_e2e_123", 123), requestRow("req_e2e_234", 234)],
    }, cursorPagination(url, secondPage ? 1 : 2, 3, !secondPage));
  }
  return commonResponse(endpoint, { rows: [row(endpoint)] });
}

function longProviderModelLabels() {
  return [
    "fireworks/accounts/fireworks/models/deepseek-v4-flash",
    "fireworks/accounts/fireworks/models/gpt-oss-20b",
    "moonshotai/kimi-k2.7-code",
    "minimax/minimax-m3",
    "openrouter/anthropic/claude-sonnet-4.6",
    "baseten/openai/gpt-oss-120b",
    "crusoe/zai/GLM-5.2",
    "openai/gpt-5.4-nano",
    "fireworks/accounts/fireworks/models/kimi-k2p7-code",
    "fireworks/accounts/fireworks/models/qwen3p6-plus",
  ];
}

function longProviderModelChart(): ReportChart {
  return {
    chart_id: "provider-model-mix-requests",
    title: "Provider/model mix requests",
    x_axis: { label: "Provider/model", type: "category" },
    y_axis: { label: "Requests", unit: "requests" },
    series: [
      {
        name: "Requests",
        unit: "requests",
        color_key: "magenta",
        points: longProviderModelLabels().map((label, index) => ({ x: label, y: 10 - index })),
      },
    ],
  };
}

function commonResponse(report: string, extra: Record<string, unknown>, pagination = topNPagination(1, true)) {
  return {
    period: { from: "2026-06-28T11:00:00Z", to: generatedUtc },
    generatedUtc,
    report,
    summary: {
      requests: 2,
      errors: 0,
      totalTokens: 345,
      inputTokens: 123,
      outputTokens: 222,
      totalCostUsd: 0.0123,
      avgLatencyMs: 245,
    },
    charts: [chart(report)],
    pagination,
    ...extra,
  };
}

function topNPagination(returned: number, hasMore = false) {
  return {
    limit: 50,
    returned,
    total_count: null,
    has_more: hasMore,
    sort: "requests",
    direction: "desc",
    mode: "top_n",
    note: "Aggregate rows are top-N for the selected filters.",
  };
}

function cursorPagination(url: URL, returned: number, totalCount: number, hasMore: boolean, defaultSort = "timeUtc") {
  const limit = Number(url.searchParams.get("limit") || "50");
  return {
    limit,
    returned,
    total_count: totalCount,
    has_more: hasMore,
    next_cursor: hasMore ? "page-2" : undefined,
    sort: url.searchParams.get("sort") || defaultSort,
    direction: url.searchParams.get("direction") || "desc",
    mode: "cursor",
  };
}

function invalidCursorResponse() {
  return {
    status: 400,
    body: {
      error: {
        type: "invalid-report-filter",
        message: "invalid cursor",
      },
    },
  };
}

function row(key: string): ReportRow {
  return {
    key,
    secondaryKey: "mock",
    requests: 2,
    attempts: 2,
    streams: 1,
    errors: 0,
    errorRatePct: 0,
    fallbacks: 0,
    fallbackRatePct: 0,
    inputTokens: 123,
    outputTokens: 222,
    totalTokens: 345,
    inputCostUsd: 0.001,
    outputCostUsd: 0.0113,
    totalCostUsd: 0.0123,
    avgLatencyMs: 245,
    avgUpstreamTokensPerSec: 31.5,
  };
}

function savingsRow(key: string): ReportRow {
  return {
    key,
    requests: 2,
    input_tokens: 123,
    output_tokens: 222,
    total_tokens: 345,
    actual_cost_usd: 0.0123,
    baseline_cost_usd: 0.015,
    savings_usd: 0.0027,
    savings_pct: 18,
  };
}

function requestRow(requestId = "req_e2e_123", cost = 123): ReportRow {
  return {
    timeUtc: generatedUtc,
    requestId,
    callerId: "alice",
    callerIp: "203.0.113.10",
    tokenId: "rtr_mock_public",
    client: "codex-cli",
    requestedModel: "default",
    modelGroup: "default",
    provider: "mock",
    model: "mock-model",
    dialect: "openai",
    status: 200,
    cache: "miss",
    attempts: 1,
    fallback: false,
    inputTokens: 123,
    outputTokens: 222,
    totalTokens: 345,
    totalCostUsd: cost / 10000,
    latencyMs: 245,
  };
}

function chart(id: string): ReportChart {
	return {
		chart_id: `${id}-requests`,
    title: `${id} requests`,
    x_axis: { label: "Time", type: "time" },
    y_axis: { label: "Requests", unit: "requests" },
    series: [
      {
        name: "Requests",
        unit: "requests",
        color_key: "magenta",
        points: [
          { x: "2026-06-28T11:00:00Z", y: 1 },
          { x: generatedUtc, y: 2 },
        ],
      },
		],
	};
}

function savingsBreakdownCharts(id: string): ReportChart[] {
	return [
		chart(id),
		{
			chart_id: `${id}_cost`,
			title: "Actual vs baseline cost",
			x_axis: { label: "Key", type: "category" },
			y_axis: { label: "USD", unit: "usd" },
			series: [
				{ name: "Actual cost", unit: "usd", color_key: "red", points: [{ x: "rtr_mock_public", y: 0.05 }] },
				{ name: "Baseline cost", unit: "usd", color_key: "blue", points: [{ x: "rtr_mock_public", y: 0.25 }] },
			],
		},
		{
			chart_id: `${id}_savings`,
			title: "Savings",
			x_axis: { label: "Key", type: "category" },
			y_axis: { label: "USD", unit: "usd" },
			series: [{ name: "Savings", unit: "usd", color_key: "magenta", points: [{ x: "rtr_mock_public", y: 0.2 }] }],
		},
		{
			chart_id: `${id}_savings_pct`,
			title: "Savings rate",
			x_axis: { label: "Key", type: "category" },
			y_axis: { label: "Percent", unit: "percent" },
			series: [{ name: "Savings rate", unit: "percent", color_key: "purple", points: [{ x: "rtr_mock_public", y: 80 }] }],
		},
	];
}
