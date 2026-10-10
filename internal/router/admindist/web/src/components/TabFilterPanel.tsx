// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useId, useState } from "react";
import type { Dispatch, ReactNode, SetStateAction } from "react";
import { ChevronDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { cacheFilterOptions, directionFilterOptions, statusFilterOptions, tabFilterDefaults, tabFilterLabels, type TabFilterName } from "@/lib/filters";
import type { ReportColumn, ReportFilters, TabSpec } from "@/lib/reports";
import { cn } from "@/lib/utils";

type Props = {
  tab: TabSpec;
  columns: ReportColumn[];
  supportedSortKeys?: ReadonlySet<string>;
  filters: ReportFilters;
  onFiltersChange: Dispatch<SetStateAction<ReportFilters>>;
};

export function TabFilterPanel({ tab, columns, supportedSortKeys, filters, onFiltersChange }: Props) {
  const visibleFilters = (tab.filters || []).filter((name) => name !== "limit");
  const [open, setOpen] = useState(() => readOpenState(tab.id));
  const panelId = useId();

  useEffect(() => {
    setOpen(readOpenState(tab.id));
  }, [tab.id]);

  useEffect(() => {
    writeOpenState(tab.id, open);
  }, [open, tab.id]);

  if (visibleFilters.length === 0) return null;

  function updateFilter(name: TabFilterName, value: string) {
    onFiltersChange((current) => ({ ...current, [name]: value }));
  }

  function resetFilters() {
    onFiltersChange((current) => {
      const next = { ...current };
      for (const name of visibleFilters) next[name] = tabFilterDefaults[name];
      return next;
    });
  }

  return (
    <section className="rounded-lg border border-white/10 bg-white/2.5 p-3" aria-label={`${tab.label} filters`}>
      <div className="flex flex-wrap items-center justify-between gap-2 lg:hidden">
        <Button type="button" variant="outline" aria-expanded={open} aria-controls={panelId} onClick={() => setOpen((value) => !value)}>
          Filters
          <ChevronDown className={cn("ml-2 h-4 w-4 transition-transform motion-reduce:transition-none", open && "rotate-180")} aria-hidden="true" />
        </Button>
        <button type="button" className="text-sm text-white/58 underline decoration-white/20 underline-offset-4 hover:text-white" onClick={resetFilters}>
          Reset filters
        </button>
      </div>
      <div id={panelId} className={cn("gap-2 lg:grid lg:grid-cols-4 xl:grid-cols-6", open ? "grid pt-3 lg:pt-0" : "hidden lg:grid")}>
        {visibleFilters.map((name) => (
          <TabFilterControl key={name} name={name} columns={columns} supportedSortKeys={supportedSortKeys} value={filters[name] ?? tabFilterDefaults[name]} onChange={updateFilter} />
        ))}
        <div className="hidden items-end lg:flex">
          <button type="button" className="h-9 text-sm text-white/58 underline decoration-white/20 underline-offset-4 hover:text-white" onClick={resetFilters}>
            Reset filters
          </button>
        </div>
      </div>
    </section>
  );
}

type ControlProps = {
  name: TabFilterName;
  columns: ReportColumn[];
  supportedSortKeys?: ReadonlySet<string>;
  value: string;
  onChange: (name: TabFilterName, value: string) => void;
};

function TabFilterControl({ name, columns, supportedSortKeys, value, onChange }: ControlProps) {
  const label = tabFilterLabels[name];
  if (name === "cache") {
    return (
      <FilterLabel label={label} name={name}>
        <Select data-tab-filter={name} name={name} value={value} onChange={(event) => onChange(name, event.target.value)}>
          {cacheFilterOptions.map(([optionValue, optionLabel]) => (
            <option key={optionValue || "any"} value={optionValue}>
              {optionLabel}
            </option>
          ))}
        </Select>
      </FilterLabel>
    );
  }
  if (name === "direction") {
    return (
      <FilterLabel label={label} name={name}>
        <Select data-tab-filter={name} name={name} value={value} onChange={(event) => onChange(name, event.target.value)}>
          {directionFilterOptions.map(([optionValue, optionLabel]) => (
            <option key={optionValue || "default"} value={optionValue}>
              {optionLabel}
            </option>
          ))}
        </Select>
      </FilterLabel>
    );
  }
  if (name === "status") {
    return (
      <FilterLabel label={label} name={name}>
        <Select data-tab-filter={name} name={name} value={value} onChange={(event) => onChange(name, event.target.value)}>
          {statusFilterOptions.map(([optionValue, optionLabel]) => (
            <option key={optionValue || "any"} value={optionValue}>
              {optionLabel}
            </option>
          ))}
        </Select>
      </FilterLabel>
    );
  }
  if (name === "sort") {
    const sortColumns = supportedSortKeys ? columns.filter((column) => supportedSortKeys.has(column.key)) : columns;
    return (
      <FilterLabel label={label} name={name}>
        <Select data-tab-filter={name} name={name} value={value} onChange={(event) => onChange(name, event.target.value)}>
          <option value="">Default</option>
          {sortColumns.map((column) => (
            <option key={column.key} value={column.key}>
              {column.label}
            </option>
          ))}
          {value && !sortColumns.some((column) => column.key === value) ? <option value={value}>{value}</option> : null}
        </Select>
      </FilterLabel>
    );
  }
  return (
    <FilterLabel label={label} name={name}>
      <Input data-tab-filter={name} name={name} value={value} placeholder={tabFilterDefaults[name] || "any"} onChange={(event) => onChange(name, event.target.value)} />
    </FilterLabel>
  );
}

type FilterLabelProps = {
  label: string;
  name: string;
  children: ReactNode;
};

function FilterLabel({ label, name, children }: FilterLabelProps) {
  return (
    <label className="grid gap-1 font-mono text-[0.68rem] uppercase text-white/58" data-tab-filter-label={name}>
      {label}
      {children}
    </label>
  );
}

function readOpenState(tabId: string) {
  try {
    return window.localStorage.getItem(storageKey(tabId)) !== "false";
  } catch {
    return true;
  }
}

function writeOpenState(tabId: string, open: boolean) {
  try {
    window.localStorage.setItem(storageKey(tabId), String(open));
  } catch {
    // Ignore storage failures so private browsing modes do not break report filters.
  }
}

function storageKey(tabId: string) {
  return `admin-tab-filters-open-${tabId}-v1`;
}
