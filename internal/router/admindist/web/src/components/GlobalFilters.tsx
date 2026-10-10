// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useId, useState } from "react";
import type { FormEvent } from "react";
import { ChevronDown, Menu } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { globalFilterFields, tabFilterFields } from "@/lib/filters";
import type { ReportFilters } from "@/lib/reports";
import { cn } from "@/lib/utils";

const storageKey = "admin-global-filters-open-v1";

type Props = {
  draftFilters: ReportFilters;
  filters: ReportFilters;
  onDraftChange: (filters: ReportFilters | ((current: ReportFilters) => ReportFilters)) => void;
  onClearTabFilters: () => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  onOpenMobileNav: () => void;
};

export function GlobalFilters({ draftFilters, filters, onDraftChange, onClearTabFilters, onSubmit, onOpenMobileNav }: Props) {
  const [expanded, setExpanded] = useState(() => {
    try {
      return window.localStorage.getItem(storageKey) === "true";
    } catch {
      return false;
    }
  });
  const panelId = useId();
  const [sinceField, ...disclosedFields] = globalFilterFields;
  const activeTabFilterCount = tabFilterFields.filter(([name, , defaultValue]) => {
    const value = filters[name]?.trim() || "";
    return value !== "" && value !== defaultValue;
  }).length;
  const markdownParams = new URLSearchParams(
    Object.entries(filters).filter(([key, value]) => value.trim() && key !== "cursor" && key !== "offset"),
  );

  useEffect(() => {
    try {
      window.localStorage.setItem(storageKey, String(expanded));
    } catch {
      // Ignore storage failures so private browsing modes do not break the form.
    }
  }, [expanded]);

  return (
    <form className="flex flex-col gap-3" onSubmit={onSubmit}>
      <div className="flex w-full flex-wrap items-end gap-2">
        <Button type="button" variant="outline" aria-expanded={expanded} aria-controls={panelId} onClick={() => setExpanded((open) => !open)}>
          Filters
          <ChevronDown className={cn("ml-2 h-4 w-4 transition-transform motion-reduce:transition-none", expanded && "rotate-180")} aria-hidden="true" />
        </Button>
        <FilterInput field={sinceField} value={draftFilters[sinceField[0]]} onDraftChange={onDraftChange} className="w-32 sm:mr-auto" />
        <Button type="submit">Apply</Button>
        <a
          className="inline-flex h-9 items-center rounded-md border border-white/15 px-3 text-sm text-white hover:bg-white/8 focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-metrum-blue"
          href={`export.md?${markdownParams}`}
          aria-label="Markdown full report for current filters"
          title="Markdown full report for current filters"
        >
          Markdown report
        </a>
        {activeTabFilterCount > 0 ? (
          <Button type="button" variant="outline" onClick={onClearTabFilters}>
            Clear tab filters ({activeTabFilterCount})
          </Button>
        ) : null}
        <Button type="button" variant="outline" className="lg:hidden" aria-label="Open report navigation" onClick={onOpenMobileNav}>
          <Menu className="mr-2 h-4 w-4" aria-hidden="true" />
          Sections
        </Button>
      </div>
      <div
        id={panelId}
        className={cn(
          "w-full gap-2 overflow-hidden transition-opacity duration-150 motion-reduce:transition-none sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4",
          expanded ? "grid opacity-100" : "hidden opacity-0",
        )}
      >
        {disclosedFields.map((field) => (
          <FilterInput key={field[0]} field={field} value={draftFilters[field[0]]} onDraftChange={onDraftChange} />
        ))}
      </div>
    </form>
  );
}

type FilterInputProps = {
  field: (typeof globalFilterFields)[number];
  value?: string;
  className?: string;
  onDraftChange: (filters: ReportFilters | ((current: ReportFilters) => ReportFilters)) => void;
};

function FilterInput({ field, value, className, onDraftChange }: FilterInputProps) {
  const [name, label, defaultValue] = field;
  return (
    <label className={cn("grid gap-1 font-mono text-[0.68rem] uppercase text-white/58", className)}>
      {label}
      <Input
        name={name}
        value={value ?? defaultValue}
        placeholder={defaultValue || "any"}
        onChange={(event) => onDraftChange((current) => ({ ...current, [name]: event.target.value }))}
      />
    </label>
  );
}
