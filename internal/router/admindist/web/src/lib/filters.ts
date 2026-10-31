// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

export type FilterField<Name extends string = string> = readonly [name: Name, label: string, defaultValue: string];

export type GlobalFilterName =
  | "since"
  | "caller_id"
  | "caller_user"
  | "token_id"
  | "caller_ip"
  | "caller_project"
  | "caller_environment"
  | "requested_model"
  | "endpoint"
  | "resolved_group"
  | "provider"
  | "target_model"
  | "dialect"
  | "client"
  | "reasoning_present"
  | "cache_present"
  | "usage_source"
  | "error_class"
  | "request_shape_fingerprint"
  | "tool_schema_fingerprint";

export type TabFilterName = "history_source" | "baseline" | "status" | "cache" | "sort" | "direction" | "traffic_shape_bucket" | "traffic_shape_scope" | "scope" | "release" | "state" | "type" | "date" | "limit";

export type PaginationFilterName = "cursor" | "offset";

export type ReportFilterName = GlobalFilterName | TabFilterName | PaginationFilterName;

export const globalFilterFields = [
  ["since", "Since", "24h"],
  ["caller_id", "Caller", ""],
  ["caller_user", "User", ""],
  ["token_id", "Token ID", ""],
  ["caller_ip", "IP", ""],
  ["caller_project", "Project", ""],
  ["caller_environment", "Environment", ""],
  ["requested_model", "Requested", ""],
  ["endpoint", "Endpoint (e.g. embeddings)", ""],
  ["resolved_group", "Group", ""],
  ["provider", "Provider", ""],
  ["target_model", "Target", ""],
  ["dialect", "Dialect", ""],
  ["client", "Client", ""],
  ["reasoning_present", "Reasoning present (true/false)", ""],
  ["cache_present", "Cache usage reported (true/false)", ""],
  ["usage_source", "Usage source (reported/partial/estimated/none)", ""],
  ["error_class", "Error class", ""],
  ["request_shape_fingerprint", "Shape FP", ""],
  ["tool_schema_fingerprint", "Tool FP", ""],
] as const satisfies ReadonlyArray<FilterField<GlobalFilterName>>;

export const tabFilterFields = [
 ["history_source", "History source (raw/rollup; whole UTC days)", ""],
  ["baseline", "Baseline", ""],
  ["status", "Status", ""],
  ["cache", "Cache", ""],
  ["traffic_shape_bucket", "Shape bucket", ""],
  ["traffic_shape_scope", "Shape scope", ""],
  ["sort", "Sort", ""],
  ["direction", "Direction", ""],
  ["limit", "Rows", "50"],
  ["scope", "Scope", ""],
  ["release", "Release", ""],
  ["state", "State", ""],
  ["type", "Type", ""],
  ["date", "Date", ""],
] as const satisfies ReadonlyArray<FilterField<TabFilterName>>;

export const paginationFilterFields = [
  ["cursor", "Cursor", ""],
  ["offset", "Offset", ""],
] as const satisfies ReadonlyArray<FilterField<PaginationFilterName>>;

export const filterFields = [...globalFilterFields, ...tabFilterFields, ...paginationFilterFields] as const satisfies ReadonlyArray<FilterField<ReportFilterName>>;

export const tabFilterLabels = Object.fromEntries(tabFilterFields.map(([name, label]) => [name, label])) as Record<TabFilterName, string>;
export const tabFilterDefaults = Object.fromEntries(tabFilterFields.map(([name, , defaultValue]) => [name, defaultValue])) as Record<TabFilterName, string>;

export const cacheFilterOptions = [
  ["", "Any cache state"],
  ["hit", "Hit"],
  ["miss", "Miss"],
  ["bypass", "Bypass"],
] as const;

export const directionFilterOptions = [
  ["", "Default"],
  ["desc", "Descending"],
  ["asc", "Ascending"],
] as const;

export const statusFilterOptions = [
  ["", "Any status"],
  ["200", "200 OK"],
  ["400", "400"],
  ["401", "401"],
  ["403", "403"],
  ["404", "404"],
  ["429", "429"],
  ["500", "500"],
] as const;

export function validateFilterModel() {
  const names = filterFields.map(([name]) => name);
  const globalNames = globalFilterFields.map(([name]) => name);
  const tabNames = tabFilterFields.map(([name]) => name);
  return {
    duplicateNames: names.filter((name, index) => names.indexOf(name) !== index),
    overlappingNames: globalNames.filter((name) => tabNames.includes(name as TabFilterName)),
    hasEveryGlobal: globalNames.every((name) => names.includes(name)),
    hasEveryTab: tabNames.every((name) => names.includes(name)),
  };
}
