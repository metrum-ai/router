// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useMemo, useState } from "react";
import { ChevronsLeft, ChevronsRight, ChevronLeft, ChevronRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import type { ReportColumn, ReportPageAction, ReportPagination, ReportRow } from "@/lib/reports";
import { formatValue, numberFmt } from "@/lib/utils";

type Props = {
  rows: ReportRow[];
  columns: ReportColumn[];
  initialSortKey?: string;
  initialSortDir?: "asc" | "desc";
  limit?: string;
  pagination?: ReportPagination;
  supportedSortKeys?: ReadonlySet<string>;
  pageIndex?: number | null;
  canGoBack?: boolean;
  loading?: boolean;
  onLimitChange?: (limit: string) => void;
  onPageChange?: (action: ReportPageAction, cursor?: string) => void;
  onSortChange?: (sort: string, direction: "asc" | "desc") => void;
  onRefresh?: () => void;
};

const limitOptions = ["25", "50", "100", "250"];

export function DataTable({
  rows,
  columns,
  initialSortKey = "",
  initialSortDir = "desc",
  limit = "50",
  pagination,
  supportedSortKeys,
  pageIndex = 0,
  canGoBack = false,
  loading = false,
  onLimitChange,
  onPageChange,
  onSortChange,
  onRefresh,
}: Props) {
  const [quickFilter, setQuickFilter] = useState("");
  const [sortKey, setSortKey] = useState(initialSortKey);
  const [sortDir, setSortDir] = useState<"asc" | "desc">(initialSortDir);
  const paginationMode = pagination?.mode || "";
  const serverPaged = paginationMode === "cursor";
  const topN = paginationMode === "top_n";

  useEffect(() => {
    setSortKey(initialSortKey);
    setSortDir(initialSortDir);
  }, [initialSortDir, initialSortKey]);

  const visibleRows = useMemo(() => {
    const needle = quickFilter.trim().toLowerCase();
    let out = needle
      ? rows.filter((row) => Object.values(row).some((value) => String(value ?? "").toLowerCase().includes(needle)))
      : rows;
    if (!serverPaged && sortKey) {
      out = [...out].sort((a, b) => {
        const cmp = compareValues(a[sortKey], b[sortKey]);
        return sortDir === "asc" ? cmp : -cmp;
      });
    }
    return out;
  }, [quickFilter, rows, serverPaged, sortDir, sortKey]);

  function sort(column: string) {
    if (serverPaged && supportedSortKeys && !supportedSortKeys.has(column)) return;
    const nextDir = sortKey === column && sortDir === "desc" ? "asc" : "desc";
    if (sortKey === column) {
      setSortDir(nextDir);
    } else {
      setSortKey(column);
      setSortDir("desc");
    }
    if (serverPaged) {
      onSortChange?.(column, nextDir);
      return;
    }
  }

  function exportCsv() {
    const header = columns.map((column) => csvCell(column.label)).join(",");
    const body = visibleRows
      .map((row) => columns.map((column) => csvCell(row[column.key] ?? "")).join(","))
      .join("\n");
    const blob = new Blob([header, "\n", body], { type: "text/csv" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "admin-report.csv";
    a.click();
    URL.revokeObjectURL(url);
  }

  function csvCell(value: unknown) {
    const text = String(value ?? "");
    const trimmed = text.trimStart();
    const safe = trimmed && ["=", "+", "-", "@"].includes(trimmed[0]) ? `'${text}` : text;
    return JSON.stringify(safe);
  }

  function renderCell(row: ReportRow, column: ReportColumn) {
    const value = row[column.key];
    const formatted = formatValue(value, column.key);
    if ((column.key === "requestId" || column.key === "request_id") && value) {
      return (
        <a className="font-mono text-metrum-pink underline decoration-metrum-pink/40 underline-offset-4 hover:text-white" href={`api/request/${encodeURIComponent(String(value))}`}>
          {formatted}
        </a>
      );
    }
    return formatted;
  }

  if (columns.length === 0) {
    return <div className="rounded-lg border border-white/10 p-6 text-sm text-white/62">{emptyMessage(rows.length, visibleRows.length, quickFilter, serverPaged)}</div>;
  }

  const limitValue = limit || String(pagination?.limit || 50);
  const hasCurrentCursor = pageIndex === null || canGoBack;
  const canPrevious = !loading && Boolean(pagination?.prev_cursor || canGoBack);
  const canNext = !loading && Boolean(pagination?.has_more && pagination?.next_cursor);
  const countLabel = paginationSummary(pagination, visibleRows.length, rows.length, pageIndex, quickFilter);
  const quickFilterLabel = topN ? "Filter returned top-N rows" : serverPaged ? "Filter current page rows" : "Search visible rows";
  const csvLabel = topN ? "CSV top-N rows" : serverPaged ? "CSV current page" : "CSV visible rows";

  return (
    <section className="space-y-3" aria-busy={loading}>
      <div className="flex flex-wrap items-end gap-2">
        <label className="grid min-w-56 gap-1 font-mono text-[0.68rem] uppercase text-white/58">
          {quickFilterLabel}
          <Input className="max-w-xs" placeholder={quickFilterLabel} value={quickFilter} onChange={(event) => setQuickFilter(event.target.value)} />
        </label>
        <label className="grid gap-1 font-mono text-[0.68rem] uppercase text-white/58">
          Rows
          <Select aria-label="Rows" value={limitValue} onChange={(event) => onLimitChange?.(event.target.value)} disabled={loading}>
            {limitOptions.map((option) => (
              <option key={option} value={option}>
                {option} rows
              </option>
            ))}
            {limitValue && !limitOptions.includes(limitValue) ? <option value={limitValue}>{limitValue} rows</option> : null}
          </Select>
        </label>
        {serverPaged ? (
          <nav className="flex items-end gap-1" aria-label="Table pagination">
            <Button type="button" variant="outline" className="w-9 px-0" aria-label="First page" disabled={loading || !hasCurrentCursor} onClick={() => onPageChange?.("first")}>
              <ChevronsLeft className="h-4 w-4" aria-hidden="true" />
            </Button>
            <Button type="button" variant="outline" className="w-9 px-0" aria-label="Previous page" disabled={!canPrevious} onClick={() => onPageChange?.("previous", pagination?.prev_cursor)}>
              <ChevronLeft className="h-4 w-4" aria-hidden="true" />
            </Button>
            <Button type="button" variant="outline" className="w-9 px-0" aria-label="Next page" disabled={!canNext} onClick={() => onPageChange?.("next", pagination?.next_cursor)}>
              <ChevronRight className="h-4 w-4" aria-hidden="true" />
            </Button>
            <Button type="button" variant="outline" className="w-9 px-0" aria-label="Last page unavailable for cursor pagination" disabled title="Last page requires a server-provided cursor or offset">
              <ChevronsRight className="h-4 w-4" aria-hidden="true" />
            </Button>
          </nav>
        ) : null}
        <Button type="button" variant="outline" onClick={onRefresh}>
          Refresh
        </Button>
        <Button type="button" variant="outline" onClick={exportCsv}>
          {csvLabel}
        </Button>
      </div>
      <p className="text-xs text-white/52" role="status" aria-live="polite">
        {countLabel}
      </p>
      <div className="overflow-auto rounded-lg border border-white/10">
        <table className="w-full min-w-[920px] border-collapse text-left text-sm">
          <thead className="bg-white/6 text-xs uppercase text-white/60">
            <tr>
              {columns.map((column) => (
                <th key={column.key} className="whitespace-nowrap px-3 py-2" aria-sort={sortKey === column.key ? (sortDir === "asc" ? "ascending" : "descending") : "none"}>
                  {serverPaged && supportedSortKeys && !supportedSortKeys.has(column.key) ? (
                    <span title={column.description || "This column is not server-sortable on paged reports"}>
                      {column.label}
                      {column.unit ? <span className="ml-1 normal-case text-white/40">({column.unit})</span> : null}
                    </span>
                  ) : (
                    <button type="button" className="text-left hover:text-white" onClick={() => sort(column.key)} title={sortTitle(column, serverPaged, topN)}>
                      {column.label}
                      {column.unit ? <span className="ml-1 normal-case text-white/40">({column.unit})</span> : null}
                      {sortKey === column.key ? <span className="ml-1 text-white/70">{sortDir === "asc" ? "▲" : "▼"}</span> : null}
                    </button>
                  )}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {visibleRows.map((row, index) => (
              <tr key={index} className="border-t border-white/10 odd:bg-white/2.5">
                {columns.map((column) => (
                  <td key={column.key} className="max-w-[360px] truncate px-3 py-2 text-white/82" title={formatValue(row[column.key], column.key)}>
                    {renderCell(row, column)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {visibleRows.length === 0 ? <p className="text-xs text-white/45">{emptyMessage(rows.length, visibleRows.length, quickFilter, serverPaged)}</p> : null}
    </section>
  );
}

function compareValues(left: unknown, right: unknown) {
  const leftNumber = numericValue(left);
  const rightNumber = numericValue(right);
  if (leftNumber !== null && rightNumber !== null) return leftNumber - rightNumber;
  return String(left ?? "").localeCompare(String(right ?? ""), undefined, { numeric: true, sensitivity: "base" });
}

function numericValue(value: unknown) {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string") {
    const normalized = value.replace(/[$,%\s]/g, "");
    if (normalized !== "") {
      const parsed = Number(normalized);
      if (Number.isFinite(parsed)) return parsed;
    }
  }
  return null;
}

function paginationSummary(pagination: ReportPagination | undefined, visibleCount: number, returnedRows: number, pageIndex: number | null, quickFilter: string) {
  const returned = pagination?.returned ?? returnedRows;
  const limit = pagination?.limit || returnedRows || 0;
  const hasQuickFilter = quickFilter.trim() !== "";
  const matchSuffix = hasQuickFilter && visibleCount !== returned ? `; ${numberFmt.format(visibleCount)} matching visible filter` : "";

  if (pagination?.mode === "top_n") {
    const more = pagination.has_more ? ", more available" : "";
    return `Showing top ${numberFmt.format(returned)} rows${more}${matchSuffix}.`;
  }
  if (pagination?.mode === "cursor") {
    if (typeof pagination.total_count === "number") {
      if (pageIndex === null) return `Showing ${numberFmt.format(returned)} rows of ${numberFmt.format(pagination.total_count)} from a bookmarked page${matchSuffix}.`;
      const start = returned > 0 ? pageIndex * limit + 1 : 0;
      const end = returned > 0 ? start + returned - 1 : 0;
      return `Showing ${numberFmt.format(start)}-${numberFmt.format(end)} of ${numberFmt.format(pagination.total_count)}${matchSuffix}.`;
    }
    const more = pagination.has_more ? ", more available" : "";
    return `Showing ${numberFmt.format(returned)} rows${more}${matchSuffix}.`;
  }
  return `Showing ${numberFmt.format(visibleCount)} of ${numberFmt.format(returnedRows)} rows.`;
}

function emptyMessage(rowCount: number, visibleCount: number, quickFilter: string, serverPaged: boolean) {
  if (quickFilter.trim() && rowCount > 0 && visibleCount === 0) return "No rows match the visible-row filter.";
  if (serverPaged) return "No rows on this page. Try the first page or broader filters.";
  return "No rows for the selected filters.";
}

function sortTitle(column: ReportColumn, serverPaged: boolean, topN: boolean) {
  if (serverPaged) return column.description || `Sort all matching rows by ${column.label}`;
  if (topN) return column.description || `Sort returned top-N rows by ${column.label}`;
  return column.description;
}
