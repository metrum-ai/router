// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useMemo, useRef, useState } from "react";
import { GlobalFilters } from "@/components/GlobalFilters";
import { MobileNavDrawer } from "@/components/MobileNavDrawer";
import { ReportPanel } from "@/components/ReportPanel";
import { Sidebar } from "@/components/Sidebar";
import { tabFilterFields } from "@/lib/filters";
import { fetchReport, fetchVersion, filterFields, tabSpecs, type ReportFilters, type ReportPageAction, type ReportResponse, type TabSpec, type VersionResponse } from "@/lib/reports";

function filtersFromUrl(): ReportFilters {
  const params = new URLSearchParams(window.location.search);
  const filters: ReportFilters = {};
  for (const [name, , defaultValue] of filterFields) {
    filters[name] = params.get(name) || defaultValue;
  }
  return filters;
}

function activeTabFromUrl() {
  const tab = new URLSearchParams(window.location.search).get("tab");
  return tabSpecs.some((spec) => spec.id === tab) ? tab || "groups" : "groups";
}

function resetPagination(filters: ReportFilters): ReportFilters {
  return { ...filters, cursor: "", offset: "" };
}

type PageHistory = {
  previousCursors: string[];
  pageIndex: number | null;
};

export default function App() {
  const [activeTab, setActiveTab] = useState(activeTabFromUrl);
  const [filters, setFilters] = useState<ReportFilters>(filtersFromUrl);
  const [draftFilters, setDraftFilters] = useState<ReportFilters>(filtersFromUrl);
  const [report, setReport] = useState<ReportResponse>();
  const [version, setVersion] = useState<VersionResponse>();
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const loadSequence = useRef(0);
  const [pageHistory, setPageHistory] = useState<PageHistory>(() => ({
    previousCursors: [],
    pageIndex: filtersFromUrl().cursor ? null : 0,
  }));

  const tab = useMemo(() => tabSpecs.find((spec) => spec.id === activeTab) || tabSpecs[1], [activeTab]);

  async function load(nextTab: TabSpec = tab, nextFilters: ReportFilters = filters) {
    const sequence = ++loadSequence.current;
    setLoading(true);
    setError("");
    try {
      const endpoint = nextTab.endpoint === "overview" ? "summary" : nextTab.endpoint;
      const nextReport = await fetchReport(endpoint, nextFilters);
      if (sequence !== loadSequence.current) return;
      setReport(nextReport);
    } catch (err) {
      if (sequence !== loadSequence.current) return;
      setError(err instanceof Error ? err.message : "Report request failed");
      setReport(undefined);
    } finally {
      if (sequence === loadSequence.current) setLoading(false);
    }
  }

  useEffect(() => {
    void fetchVersion()
      .then(setVersion)
      .catch(() => setVersion(undefined));
  }, []);

  useEffect(() => {
    const url = new URL(window.location.href);
    url.searchParams.set("tab", activeTab);
    for (const [key, value] of Object.entries(filters)) {
      if (value.trim()) url.searchParams.set(key, value.trim());
      else url.searchParams.delete(key);
    }
    history.replaceState(null, "", url);
    void load(tab, filters);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeTab, filters]);

  function applyFilters(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const next = resetPagination(draftFilters);
    setFilters(next);
    setDraftFilters(next);
    setPageHistory({ previousCursors: [], pageIndex: 0 });
  }

  function updateFilters(updater: React.SetStateAction<ReportFilters>) {
    const apply = (current: ReportFilters) => resetPagination(typeof updater === "function" ? updater(current) : updater);
    setFilters((current) => apply(current));
    setDraftFilters((current) => apply(current));
    setPageHistory({ previousCursors: [], pageIndex: 0 });
  }

  function clearTabFilters() {
    const clear = (current: ReportFilters) => {
      const next = { ...current };
      for (const [name, , defaultValue] of tabFilterFields) next[name] = defaultValue;
      return resetPagination(next);
    };
    setDraftFilters(clear);
    setFilters(clear);
    setPageHistory({ previousCursors: [], pageIndex: 0 });
  }

  function changeTab(tabId: string) {
    setActiveTab(tabId);
    setFilters((current) => resetPagination(current));
    setDraftFilters((current) => resetPagination(current));
    setPageHistory({ previousCursors: [], pageIndex: 0 });
  }

  function changePage(action: ReportPageAction, cursor?: string) {
    if (action === "first") {
      setFilters((current) => resetPagination(current));
      setPageHistory({ previousCursors: [], pageIndex: 0 });
      return;
    }
    if (action === "next" && cursor) {
      const currentCursor = filters.cursor || "";
      setFilters((current) => ({ ...current, cursor, offset: "" }));
      setPageHistory((history) => ({
        previousCursors: [...history.previousCursors, currentCursor],
        pageIndex: history.pageIndex === null ? null : history.pageIndex + 1,
      }));
      return;
    }
    if (action === "previous") {
      const previousCursor = cursor || pageHistory.previousCursors[pageHistory.previousCursors.length - 1] || "";
      setFilters((current) => ({ ...current, cursor: previousCursor, offset: "" }));
      setPageHistory((history) => {
        const previousCursors = history.previousCursors.slice(0, -1);
        return {
          previousCursors,
          pageIndex: history.pageIndex === null ? (previousCursors.length === 0 ? 0 : null) : Math.max(0, history.pageIndex - 1),
        };
      });
    }
  }

  const versionLabel = version?.version ? `v${version.version}` : "";
  const commitLabel = version?.commit && version.commit !== "unknown" ? version.commit.slice(0, 12) : "";
  const buildLabel = version?.build_date && version.build_date !== "unknown" ? version.build_date : "";

  return (
    <div className="mx-auto w-full max-w-[1700px] space-y-5 px-4 py-5 lg:px-6">
      <header className="rounded-lg border border-white/10 bg-black p-4 shadow-2xl">
        <div className="grid gap-4">
          <div className="flex min-w-0 items-center gap-4" data-admin-header-brand>
            <img className="w-44 max-w-[42vw]" src="static/metrum_logo_white_new.png" alt="Metrum AI" />
            <div className="min-w-0">
              <p className="font-mono text-xs uppercase text-white/58">Metrum AI Router</p>
              <h1 className="font-display text-3xl text-white">Admin Reports</h1>
              <p className="text-sm text-white/58">Operational usage, savings, routing, and security reporting.</p>
              {version && (
                <div className="mt-2 flex flex-wrap gap-1.5 font-mono text-[0.68rem] uppercase text-white/66 xl:justify-end">
                  {versionLabel && <span className="rounded border border-white/14 bg-white/6 px-2 py-1 text-white">{versionLabel}</span>}
                  {commitLabel && <span className="rounded border border-white/14 bg-white/[0.035] px-2 py-1">Commit {commitLabel}</span>}
                  {buildLabel && <span className="rounded border border-white/14 bg-white/[0.035] px-2 py-1">Built {buildLabel}</span>}
                  {version.license_compile_mode && <span className="rounded border border-metrum-purple/45 bg-metrum-purple/15 px-2 py-1 text-white/80">{version.license_compile_mode}</span>}
                </div>
              )}
            </div>
          </div>
          <div className="border-t border-white/10 pt-4" data-admin-header-filters>
            <GlobalFilters
              draftFilters={draftFilters}
              filters={filters}
              onDraftChange={setDraftFilters}
              onClearTabFilters={clearTabFilters}
              onSubmit={applyFilters}
              onOpenMobileNav={() => setMobileNavOpen(true)}
            />
          </div>
        </div>
      </header>
      <div className="grid min-w-0 gap-5 lg:grid-cols-[17rem_minmax(0,1fr)]">
        <Sidebar tabs={tabSpecs} activeTab={activeTab} onTabChange={changeTab} className="sticky top-5 hidden max-h-[calc(100vh-2.5rem)] overflow-y-auto lg:block" />
        <div className="min-w-0">
          <ReportPanel
            tab={tab}
            report={report}
            filters={filters}
            pageIndex={pageHistory.pageIndex}
            canGoBack={pageHistory.previousCursors.length > 0}
            loading={loading}
            error={error}
            onFiltersChange={updateFilters}
            onPageChange={changePage}
            onRefresh={() => void load(tab, filters)}
          />
        </div>
      </div>
      <MobileNavDrawer open={mobileNavOpen} tabs={tabSpecs} activeTab={activeTab} onOpenChange={setMobileNavOpen} onTabChange={changeTab} />
    </div>
  );
}
