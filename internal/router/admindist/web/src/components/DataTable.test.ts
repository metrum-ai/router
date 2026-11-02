// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, test } from "vitest";
import { csvCell } from "./DataTable";

describe("CSV cells", () => {
  test("quotes embedded quotes, commas and newlines according to CSV", () => {
    expect(csvCell('team",=1\nnext')).toBe('"team"",=1\nnext"');
    expect(csvCell('" , =HYPERLINK("x")')).toBe('""" , =HYPERLINK(""x"")"');
  });
  test("neutralizes formula text but preserves negative numeric savings", () => {
    expect(csvCell(" \t=1+1")).toBe('"\' \t=1+1"');
    expect(csvCell("-2")).toBe('"\'-2"');
    expect(csvCell(-2)).toBe("-2");
  });
});
