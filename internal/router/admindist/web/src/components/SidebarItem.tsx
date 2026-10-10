// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import type { TabSpec } from "@/lib/reports";
import { cn } from "@/lib/utils";

type SidebarItemProps = {
  tab: TabSpec;
  active: boolean;
  showDescription?: boolean;
  onSelect: (tabId: string) => void;
};

export function SidebarItem({ tab, active, showDescription = false, onSelect }: SidebarItemProps) {
  return (
    <button
      type="button"
      data-report-tab={tab.id}
      aria-current={active ? "page" : undefined}
      title={tab.metadata.shortDescription}
      className={cn(
        "grid w-full gap-0.5 rounded-md px-3 py-2 text-left text-sm transition-colors focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-metrum-blue",
        active ? "bg-metrum-purple text-white" : "text-white/64 hover:bg-white/[0.07] hover:text-white",
      )}
      onClick={() => onSelect(tab.id)}
    >
      <span className="truncate">{tab.label}</span>
      {showDescription ? (
        <span className="line-clamp-2 text-xs normal-case text-white/48" aria-hidden="true">
          {tab.metadata.shortDescription}
        </span>
      ) : null}
    </button>
  );
}
