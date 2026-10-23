// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MetricGrid } from "./MetricGrid";
import { globalFilterFields } from "@/lib/filters";

describe("usage provenance", () => {
  it("renders estimated share including a genuinely reported zero", () => {
    for (const share of [0, 25]) {
      const html = renderToStaticMarkup(<MetricGrid summary={{ estimatedTokenShare: share }} />);
      expect(html).toContain("Estimated usage");
      expect(html).toContain(`${share.toFixed(1)}%`);
    }
  });
  it("does not invent provenance for old summaries", () => {
    expect(renderToStaticMarkup(<MetricGrid summary={{ requests: 1 }} />)).not.toContain("Estimated usage");
  });
  it("offers the persisted provenance filter", () => {
    expect(globalFilterFields.some(([name]) => name === "usage_source")).toBe(true);
  });
});
