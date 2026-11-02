// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import type { TabSpec } from "@/lib/reports";

export type NavGroupId =
  | "overview"
  | "usage"
  | "savings"
  | "performance"
  | "traffic-shaping"
  | "routing-decisions"
  | "provider-catalog"
  | "security"
  | "request-drilldown"
  | "system-status"
  | "operations";

export type NavGroup = {
  id: NavGroupId;
  label: string;
  description: string;
  tabIds: string[];
};

export const navGroups: NavGroup[] = [
  {
    id: "overview",
    label: "Overview",
    description: "Start with high-level health, cost, latency, cache, and fallback signals.",
    tabIds: ["overview"],
  },
  {
    id: "usage",
    label: "Usage",
    description: "Compare usage by group, provider, key, user, client, project, and capability.",
    tabIds: [
      "groups",
      "providers",
      "tokens",
      "model-groups-by-user",
      "usage-by-key",
      "usage-by-caller",
      "requested-models",
      "provider-model-mix",
      "client-breakdown",
      "project-chargeback",
      "capability-usage",
    ],
  },
  {
    id: "savings",
    label: "Savings",
    description: "Compare stored actual costs with selected source-dated baseline prices.",
    tabIds: ["savings", "savings-by-user", "savings-by-key", "savings-by-group", "savings-by-project", "savings-by-provider-model"],
  },
  {
    id: "performance",
    label: "Performance",
    description: "Investigate latency, throughput, errors, fallbacks, and cache behavior.",
    tabIds: ["latency-throughput", "errors-fallbacks", "upstream-failures", "request-shape-failures", "fallback-health", "user-client-impact", "cache-report"],
  },
  {
    id: "traffic-shaping",
    label: "Traffic shaping",
    description: "Review caller burst smoothing, provider capacity shaping, and adaptive backoff.",
    tabIds: [
      "traffic-shaping-overview",
      "traffic-shaping-by-user",
      "traffic-shaping-by-key",
      "traffic-shaping-by-client",
      "traffic-shaping-by-group",
      "provider-capacity-shaping",
      "adaptive-upstream-backoff",
      "traffic-tuning-advisor",
    ],
  },
  {
    id: "routing-decisions",
    label: "Routing decisions",
    description: "Inspect safe routing, scoring, thresholds, token buckets, and admission reasons.",
    tabIds: [
      "routing-decisions",
      "dynamic-signals",
      "dynamic-score-buckets",
      "dynamic-thresholds",
      "max-token-buckets",
      "input-token-buckets",
      "admission-reasons",
      "troubleshooting-buckets",
    ],
  },
  {
    id: "provider-catalog",
    label: "Provider catalog",
    description: "Review safe catalog metadata, target validation, contracts, and workloads.",
    tabIds: ["provider-catalog-status", "target-validation", "contract-buckets", "contract-workloads"],
  },
  {
    id: "security",
    label: "Security",
    description: "Review safe scalar access and authorization events.",
    tabIds: ["security-events"],
  },
  {
    id: "request-drilldown",
    label: "Request drilldown",
    description: "Open expensive, anomalous, or recent request rows for incident detail.",
    tabIds: ["expensive-requests", "requests", "anomalies"],
  },
  {
    id: "system-status",
    label: "System status",
    description: "Check quota, budget, retention, and rollup status.",
    tabIds: ["quotas-budgets", "retention-status"],
  },
  {
    id: "operations",
    label: "Operations",
    description: "Review safe migration status and release contracts; this surface has no write controls.",
    tabIds: ["data-migrations"],
  },
];

export function groupedTabs(groups: NavGroup[], tabs: TabSpec[]) {
  const byId = new Map(tabs.map((tab) => [tab.id, tab]));
  return groups.map((group) => ({
    ...group,
    tabs: group.tabIds.map((tabId) => {
      const tab = byId.get(tabId);
      if (!tab) throw new Error(`Unknown admin report tab in nav group ${group.id}: ${tabId}`);
      return tab;
    }),
  }));
}

export function validateNavGroups(groups: NavGroup[], tabs: TabSpec[]) {
  const tabIds = new Set(tabs.map((tab) => tab.id));
  const seen = new Map<string, string>();
  const duplicateTabs: string[] = [];
  const unknownTabs: string[] = [];

  for (const group of groups) {
    for (const tabId of group.tabIds) {
      if (!tabIds.has(tabId)) unknownTabs.push(tabId);
      if (seen.has(tabId)) duplicateTabs.push(tabId);
      seen.set(tabId, group.id);
    }
  }

  const missingTabs = tabs.map((tab) => tab.id).filter((tabId) => !seen.has(tabId));
  return {
    duplicateTabs,
    missingTabs,
    unknownTabs,
    uniqueGroupIds: new Set(groups.map((group) => group.id)).size === groups.length,
    nonEmptyLabels: groups.every((group) => group.label.trim().length > 0),
    nonEmptyDescriptions: groups.every((group) => group.description.trim().length > 0),
  };
}
