// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, test } from "vitest";
import { reportForTab } from "@/App";
import { markdownReportHref } from "./GlobalFilters";

describe("CFO navigation isolation", () => {
  test("routes Markdown to the CFO export with current filters", () => {
    expect(markdownReportHref("cfo", { since: "24h", cursor: "old", baseline: "fixture" })).toBe("cfo/export.md?since=24h&baseline=fixture");
    expect(markdownReportHref("keys", { since: "24h" })).toBe("export.md?since=24h");
  });
  test("does not render a previous key report while CFO is pending", () => {
    const old = { tab: "keys", report: { rows: [{ key: "legacy-secret" }], charts: [] } };
    expect(reportForTab("cfo", old)).toBeUndefined();
    expect(reportForTab("keys", old)).toBe(old.report);
    expect(reportForTab("cfo", { tab: "cfo", report: { rows: [{ key: "key-1" }] } })).toBeDefined();
  });
});
