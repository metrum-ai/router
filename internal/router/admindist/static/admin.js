const charts = {};
let activeTab = "groups";
let lastReport = null;
let lastSavings = null;
let lastGeneric = null;
let currentTableRows = [];
let currentTableColumns = [];
let currentSort = { key: "", direction: "desc" };

const themeKey = "metrum-admin-reports-theme";
const genericTabs = {
  overview: { endpoint: "overview", title: "Overview" },
  "savings-by-user": { endpoint: "savings-by-user", title: "Savings by user", savings: true },
  "savings-by-key": { endpoint: "savings-by-key", title: "Savings by key", savings: true },
  "savings-by-group": { endpoint: "savings-by-group", title: "Savings by model group", savings: true },
  "savings-by-project": { endpoint: "savings-by-project", title: "Savings by project", savings: true },
  "savings-by-provider-model": { endpoint: "savings-by-provider-model", title: "Savings by provider/model", savings: true },
  "model-groups-by-user": { endpoint: "model-groups-by-user", title: "Model groups by user" },
  "usage-by-key": { endpoint: "usage-by-key", title: "Usage per API key" },
  "usage-by-caller": { endpoint: "usage-by-caller", title: "Usage per caller" },
  "requested-models": { endpoint: "requested-models", title: "Requested models" },
  "provider-model-mix": { endpoint: "provider-model-mix", title: "Provider and model mix" },
  "latency-throughput": { endpoint: "latency-throughput", title: "Latency and throughput" },
  "errors-fallbacks": { endpoint: "errors-fallbacks", title: "Errors and fallbacks" },
  "cache-report": { endpoint: "cache", title: "Cache" },
  "quotas-budgets": { endpoint: "quotas-budgets", title: "Quotas and budgets" },
  "troubleshooting-buckets": { endpoint: "troubleshooting-buckets", title: "Troubleshooting buckets" },
  "routing-decisions": { endpoint: "routing-decisions", title: "Routing decisions" },
  "dynamic-signals": { endpoint: "dynamic-signals", title: "Dynamic signals" },
  "dynamic-score-buckets": { endpoint: "dynamic-score-buckets", title: "Dynamic score buckets" },
  "dynamic-thresholds": { endpoint: "dynamic-thresholds", title: "Dynamic thresholds" },
  "max-token-buckets": { endpoint: "max-token-buckets", title: "Max-token buckets" },
  "input-token-buckets": { endpoint: "input-token-buckets", title: "Input-token buckets" },
  "admission-reasons": { endpoint: "admission-reasons", title: "Admission reasons" },
  "provider-catalog-status": { endpoint: "provider-catalog-status", title: "Provider catalog status", catalog: true },
  "retention-status": { endpoint: "retention-status", title: "Retention and rollups", retention: true },
  "contract-buckets": { endpoint: "contract-buckets", title: "Contract buckets" },
  "contract-workloads": { endpoint: "contract-workloads", title: "Contract workloads" },
  "target-validation": { endpoint: "target-validation", title: "Target validation" },
  "expensive-requests": { endpoint: "expensive-requests", title: "Expensive requests", requests: true },
  "client-breakdown": { endpoint: "client-breakdown", title: "Client breakdown" },
  "project-chargeback": { endpoint: "project-chargeback", title: "Project chargeback" },
  "capability-usage": { endpoint: "capability-usage", title: "Capability usage" },
  "security-events": { endpoint: "security/events", title: "Security access", security: true },
  anomalies: { endpoint: "anomalies", title: "Anomalies" }
};
const fmt = new Intl.NumberFormat("en-US", { maximumFractionDigits: 2 });
const compact = new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 2 });
const usd = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 6 });
const usdCompact = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", notation: "compact", maximumFractionDigits: 2 });

function storedTheme() {
  try {
    const value = window.localStorage.getItem(themeKey);
    return value === "light" || value === "dark" ? value : "";
  } catch {
    return "";
  }
}

function systemTheme() {
  return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function currentTheme() {
  return document.documentElement.dataset.theme || storedTheme() || systemTheme();
}

function setStoredTheme(theme) {
  try {
    window.localStorage.setItem(themeKey, theme);
  } catch {
    // Theme persistence is best effort; the selected theme still applies for this page.
  }
}

function applyTheme(theme, persist) {
  document.documentElement.dataset.theme = theme;
  if (persist) setStoredTheme(theme);
  const toggle = document.querySelector("#themeToggle");
  if (toggle) {
    const isDark = theme === "dark";
    toggle.textContent = isDark ? "Light" : "Dark";
    toggle.setAttribute("aria-pressed", String(isDark));
    toggle.setAttribute("aria-label", isDark ? "Switch to light theme" : "Switch to dark theme");
  }
}

function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

function palette() {
  return {
    red: cssVar("--metrum-red"),
    pink: cssVar("--metrum-pink"),
    magenta: cssVar("--metrum-magenta"),
    purple: cssVar("--metrum-purple"),
    violet: cssVar("--metrum-violet"),
    blue: cssVar("--metrum-blue"),
    success: cssVar("--success"),
    warning: cssVar("--warning"),
    grid: cssVar("--chart-grid"),
    text: cssVar("--chart-text")
  };
}

function colorFor(key) {
  const c = palette();
  return c[key] || c.magenta;
}

function formatUnit(value, unit, exact) {
  const n = Number(value) || 0;
  if (unit === "usd") return exact ? usd.format(n) : usdCompact.format(n);
  if (unit === "tokens" || unit === "count") return exact ? fmt.format(n) : compact.format(n);
  if (unit === "ms") return n >= 1000 ? `${fmt.format(n / 1000)} s` : `${fmt.format(n)} ms`;
  if (unit === "seconds") return `${fmt.format(n)} s`;
  if (unit === "tok/s") return `${fmt.format(n)} tok/s`;
  if (unit === "percent") return `${fmt.format(n)}%`;
  return fmt.format(n);
}

function qs() {
  const data = new FormData(document.querySelector("#filters"));
  const params = new URLSearchParams();
  for (const [key, value] of data.entries()) {
    if (String(value).trim()) params.set(key, String(value).trim());
  }
  const limit = document.querySelector("#pageSize") && document.querySelector("#pageSize").value;
  if (limit) params.set("limit", limit);
  return params;
}

function syncFormToURL(url, selector) {
  const form = document.querySelector(selector);
  if (!form) return;
  for (const field of form.querySelectorAll("[name]")) {
    const key = field.name;
    const value = String(field.value || "").trim();
    if (value) url.searchParams.set(key, value); else url.searchParams.delete(key);
  }
}

function restoreFormFromURL(selector, params) {
  const form = document.querySelector(selector);
  if (!form) return;
  for (const field of form.querySelectorAll("[name]")) {
    const value = params.get(field.name);
    if (value !== null) field.value = value;
  }
}

function savingsQS() {
  const params = qs();
  const data = new FormData(document.querySelector("#savingsFilters"));
  for (const [key, value] of data.entries()) {
    if (String(value).trim()) params.set(key, String(value).trim());
  }
  return params;
}

async function load() {
  const params = qs();
  document.querySelector("#export").href = `export.md?${params}`;
  const res = await fetch(`api/summary?${params}`, { credentials: "same-origin" });
  if (!res.ok) throw new Error(`report request failed: ${res.status}`);
  const report = await res.json();
  render(report);
}

async function loadSavings() {
  const params = savingsQS();
  const res = await fetch(`api/savings?${params}`, { credentials: "same-origin" });
  if (!res.ok) throw new Error(`savings request failed: ${res.status}`);
  const report = await res.json();
  renderSavings(report);
}

async function loadGeneric(tab = activeTab) {
  const cfg = genericTabs[tab];
  if (!cfg) return;
  const params = cfg.savings ? savingsQS() : qs();
  const res = await fetch(`api/${cfg.endpoint}?${params}`, { credentials: "same-origin" });
  if (!res.ok) throw new Error(`${cfg.title} request failed: ${res.status}`);
  const report = await res.json();
  renderGeneric(tab, report);
}

function updateURLState() {
  const url = new URL(window.location.href);
  url.searchParams.set("tab", activeTab);
  syncFormToURL(url, "#filters");
  if (activeTab === "savings" || genericTabs[activeTab] && genericTabs[activeTab].savings) {
    syncFormToURL(url, "#savingsFilters");
  }
  const search = document.querySelector("#tableSearch").value.trim();
  if (search) url.searchParams.set("search", search); else url.searchParams.delete("search");
  const limit = document.querySelector("#pageSize").value;
  if (limit) url.searchParams.set("limit", limit);
  if (currentSort.key) {
    url.searchParams.set("sort", currentSort.key);
    url.searchParams.set("direction", currentSort.direction);
  } else {
    url.searchParams.delete("sort");
    url.searchParams.delete("direction");
  }
  history.replaceState(null, "", url);
}

function restoreURLState() {
  const params = new URLSearchParams(window.location.search);
  const tab = params.get("tab");
  if (tab && (genericTabs[tab] || ["groups", "providers", "tokens", "savings", "requests"].includes(tab))) {
    activeTab = tab;
  }
  const search = params.get("search");
  if (search) document.querySelector("#tableSearch").value = search;
  const limit = params.get("limit");
  if (limit) document.querySelector("#pageSize").value = limit;
  const sort = params.get("sort");
  const direction = params.get("direction");
  if (sort) currentSort = { key: sort, direction: direction === "asc" ? "asc" : "desc" };
  restoreFormFromURL("#filters", params);
  restoreFormFromURL("#savingsFilters", params);
  document.querySelectorAll(".tabs button").forEach(button => button.classList.toggle("active", button.dataset.tab === activeTab));
}

function render(report) {
  lastReport = report;
  document.querySelector("#period").textContent = `${report.period.from} to ${report.period.to}`;
  const s = report.summary;
  document.querySelector("#summary").innerHTML = [
    ["Requests", fmt.format(s.requests)],
    ["Errors", fmt.format(s.errors)],
    ["Total Tokens", fmt.format(s.totalTokens || s.tokens)],
    ["Billed cost", usd.format(s.cost_billed_usd ?? s.totalCostUsd ?? s.costUsd ?? 0)],
    ["List cost", usd.format(s.cost_list_usd || 0)],
    ["Cache savings", usd.format(s.cache_savings_usd || 0)],
    ["Avg latency", `${fmt.format(s.avgLatencyMs)} ms`],
    ["Fallbacks", fmt.format(s.fallbacks)]
  ].map(([label, value]) => `<div class="metric"><strong>${value}</strong><span>${label}</span></div>`).join("");
  renderCharts(report);
  renderTable(report);
}

function renderCharts(report) {
  dismissChartTooltip();
  if (Array.isArray(report.charts) && report.charts.length) {
    renderChartSpecs(report.charts);
    return;
  }
  const c = palette();
  chart("requestsChart", report.series.map(x => x.timeUtc), [{ label: "Requests", data: report.series.map(x => x.requests), borderColor: c.magenta }], c);
  chart("costChart", report.series.map(x => x.timeUtc), [{ label: "Total cost", data: report.series.map(x => x.costUsd), borderColor: c.red }], c);
  chart("latencyChart", report.series.map(x => x.timeUtc), [{ label: "Latency", data: report.series.map(x => x.latencyMs), borderColor: c.violet }, { label: "TTFB", data: report.series.map(x => x.ttfbMs), borderColor: c.blue }], c);
  chart("cacheChart", report.series.map(x => x.timeUtc), [{ label: "Hits", data: report.series.map(x => x.cacheHits), borderColor: c.success }, { label: "Misses", data: report.series.map(x => x.cacheMisses), borderColor: c.warning }, { label: "Bypass", data: report.series.map(x => x.cacheBypass), borderColor: c.text }], c);
  chart("providerChart", report.byProvider.map(x => x.key), [{ label: "Total Tokens", data: report.byProvider.map(x => x.totalTokens || x.tokens), borderColor: c.purple }], c);
  chart("errorChart", report.series.map(x => x.timeUtc), [{ label: "Errors", data: report.series.map(x => x.errors), borderColor: c.red }, { label: "Fallbacks", data: report.series.map(x => x.fallbacks), borderColor: c.warning }], c);
}

function renderChartSpecs(specs) {
  dismissChartTooltip();
  const byID = Object.fromEntries(specs.map(spec => [spec.chart_id, spec]));
  chartFromSpec("requestsChart", byID.requests);
  chartFromSpec("costChart", byID.cost);
  chartFromSpec("latencyChart", byID.latency);
  chartFromSpec("cacheChart", byID.cache);
  chartFromSpec("providerChart", byID.provider_tokens);
  chartFromSpec("errorChart", byID.errors_fallbacks);
}

function renderSavingsChartSpecs(specs) {
  dismissChartTooltip();
  const byID = Object.fromEntries(specs.map(spec => [spec.chart_id, spec]));
  chartFromSpec("savingsCostChart", byID.savings_cost);
  chartFromSpec("savingsUsdChart", byID.savings_usd);
  chartFromSpec("savingsPctChart", byID.savings_pct);
}

function chartFromSpec(id, spec) {
  if (!spec) return;
  const canvas = document.getElementById(id);
  const figure = canvas.closest("figure");
  const caption = figure && figure.querySelector("figcaption");
  if (caption) caption.textContent = spec.title;
  const labels = (((spec.series || [])[0] || {}).points || []).map(point => point.x);
  const datasets = (spec.series || []).map(item => ({
    label: item.name,
    data: (item.points || []).map(point => point.y),
    borderColor: colorFor(item.color_key),
    backgroundColor: colorFor(item.color_key),
    unit: item.unit,
    colorKey: item.color_key
  }));
  chart(id, labels, datasets, palette(), spec);
}

function chart(id, labels, datasets, colors, spec) {
  dismissChartTooltip();
  if (charts[id]) charts[id].destroy();
  charts[id] = new Chart(document.getElementById(id), {
    type: "line",
    data: { labels, datasets },
    options: {
      gridColor: colors.grid,
      textColor: colors.text,
      xAxis: spec ? spec.x_axis : { label: "Time", unit: "UTC hour" },
      yAxis: spec ? spec.y_axis : { label: "", unit: "" },
      formatValue: formatUnit
    }
  });
}

function dismissChartTooltip() {
  if (typeof window.hideChartTooltip === "function") window.hideChartTooltip();
}

function renderTable(report) {
  const savingsPanel = document.querySelector("#savingsPanel");
  savingsPanel.hidden = activeTab !== "savings";
  document.querySelector("#detailPanel").hidden = true;
  if (genericTabs[activeTab]) {
    document.querySelector("#tables").innerHTML = "";
    loadGeneric(activeTab).catch(err => document.querySelector("#tables").innerHTML = `<div class="error">${esc(err.message)}</div>`);
    return;
  }
  if (activeTab === "savings") {
    document.querySelector("#tables").innerHTML = "";
    loadSavings().catch(err => document.querySelector("#tables").innerHTML = `<div class="error">${esc(err.message)}</div>`);
    return;
  }
  const source = activeTab === "providers" ? report.byProvider : activeTab === "tokens" ? report.byToken : activeTab === "requests" ? report.requests : report.byGroup;
  currentTableRows = source || [];
  currentTableColumns = activeTab === "requests" ? requestColumns() : aggregateColumns();
  renderSharedTable();
  updateURLState();
}

function renderSavings(report) {
  lastSavings = report;
  populateBaselines(report.baselines || [], report.baseline && report.baseline.baseline_id);
  const s = report.summary || {};
  document.querySelector("#savingsSummary").innerHTML = [
    ["Actual cost", usd.format(s.actual_cost_usd || 0)],
    ["Baseline cost", usd.format(s.baseline_cost_usd || 0)],
    ["Savings", usd.format(s.savings_usd || 0)],
    ["Savings rate", formatUnit(s.savings_pct || 0, "percent", true)],
    ["Input Tokens", fmt.format(s.input_tokens || 0)],
    ["Output Tokens", fmt.format(s.output_tokens || 0)],
    ["Total Tokens", fmt.format(s.total_tokens || 0)]
  ].map(([label, value]) => `<div class="metric"><strong>${value}</strong><span>${label}</span></div>`).join("");
  renderSavingsChartSpecs(report.charts || []);
  const warnings = report.warnings || [];
  document.querySelector("#savingsWarnings").innerHTML = warnings.length ? `<div class="warnings">${warnings.map(w => `<div class="warning">${esc(w)}</div>`).join("")}</div>` : "";
  currentTableRows = report.byGroup || [];
  currentTableColumns = savingsColumns();
  renderSharedTable();
  updateURLState();
}

function renderGeneric(tab, report) {
  lastGeneric = { tab, report };
  const cfg = genericTabs[tab];
  document.querySelector("#savingsPanel").hidden = !cfg.savings;
  document.querySelector("#detailPanel").hidden = true;
  document.querySelector("#refreshState").textContent = `Refreshed ${new Date().toLocaleTimeString()}`;
  if (report.period) document.querySelector("#period").textContent = `${report.period.from} to ${report.period.to}`;
  if (report.summary) {
    if (cfg.security) renderSecuritySummary(report.summary);
    else if (cfg.catalog) renderCatalogSummary(report.summary);
    else renderGenericSummary(report.summary);
  } else if (cfg.retention) {
    renderRetentionSummary(report);
  }
  renderGenericCharts(report);
  if (cfg.catalog) {
    currentTableRows = report.rows || [];
    currentTableColumns = catalogColumns();
  } else if (cfg.retention) {
    currentTableRows = retentionRows(report);
    currentTableColumns = retentionColumns();
  } else if (cfg.requests) {
    currentTableRows = report.requests || [];
    currentTableColumns = requestColumns();
  } else if (cfg.security) {
    currentTableRows = report.rows || [];
    currentTableColumns = securityColumns();
  } else if (tab === "anomalies") {
    currentTableRows = report.rows || [];
    currentTableColumns = anomalyColumns();
  } else {
    currentTableRows = report.rows || [];
    currentTableColumns = scalarColumns({ includeSavings: cfg.savings });
  }
  renderSharedTable();
}

function renderGenericSummary(s) {
  document.querySelector("#summary").innerHTML = [
    ["Requests", fmt.format(s.requests || 0)],
    ["Errors", fmt.format(s.errors || 0)],
    ["Total Tokens", fmt.format(s.totalTokens || s.tokens || 0)],
    ["Total cost", usd.format(s.totalCostUsd || s.costUsd || 0)],
    ["Avg latency", `${fmt.format(s.avgLatencyMs || 0)} ms`],
    ["Fallbacks", fmt.format(s.fallbacks || 0)]
  ].map(([label, value]) => `<div class="metric"><strong>${value}</strong><span>${label}</span></div>`).join("");
}

function renderSecuritySummary(s) {
  document.querySelector("#summary").innerHTML = [
    ["Events", fmt.format(s.events || 0)],
    ["Allowed", fmt.format(s.allowed || 0)],
    ["Unauthorized", fmt.format(s.unauthorized || 0)],
    ["Forbidden", fmt.format(s.forbidden || 0)],
    ["Denied", fmt.format(s.denied || 0)],
    ["Unique IPs", fmt.format(s.uniqueIps || 0)]
  ].map(([label, value]) => `<div class="metric"><strong>${value}</strong><span>${label}</span></div>`).join("");
}

function renderCatalogSummary(s) {
  document.querySelector("#summary").innerHTML = [
    ["Providers", fmt.format(s.providers || 0)],
    ["Catalog models", fmt.format(s.catalogModels || 0)],
    ["Active targets", fmt.format(s.activeTargets || 0)],
    ["Validated targets", fmt.format(s.validatedTargets || 0)],
    ["Passed targets", fmt.format(s.passedTargets || 0)],
    ["Missing pricing", fmt.format(s.missingPricing || 0)]
  ].map(([label, value]) => `<div class="metric"><strong>${value}</strong><span>${label}</span></div>`).join("");
}

function renderRetentionSummary(report) {
  const latest = report.latestJob || {};
  document.querySelector("#summary").innerHTML = [
    ["Retention", report.enabled ? "Enabled" : "Disabled"],
    ["Mode", report.dryRun ? "Dry run" : "Delete"],
    ["Latest job", latest.status || "none"],
    ["Tables", fmt.format((report.tables || []).length)],
    ["Rollups", fmt.format((report.rollups || []).length)],
    ["Generated", report.generatedUtc || ""]
  ].map(([label, value]) => `<div class="metric"><strong>${esc(value)}</strong><span>${label}</span></div>`).join("");
}

function renderGenericCharts(report) {
  const charts = report.charts || [];
  const slots = ["requestsChart", "costChart", "latencyChart", "cacheChart", "providerChart", "errorChart"];
  slots.forEach((id, index) => {
    if (charts[index]) chartFromSpec(id, charts[index]); else clearChart(id);
  });
}

function clearChart(id) {
  dismissChartTooltip();
  if (charts[id]) {
    charts[id].destroy();
    delete charts[id];
  }
  const canvas = document.getElementById(id);
  if (!canvas) return;
  const ctx = canvas.getContext("2d");
  if (ctx) ctx.clearRect(0, 0, canvas.width, canvas.height);
  const figure = canvas.closest("figure");
  const caption = figure && figure.querySelector("figcaption");
  if (caption) caption.textContent = "No chart";
}

function renderSharedTable() {
  const rows = visibleRows();
  if (!rows.length) {
    document.querySelector("#tables").innerHTML = `<div class="tablewrap"><table><tbody><tr><td>No rows match the current filters.</td></tr></tbody></table></div>`;
    return;
  }
  const head = currentTableColumns.map(col => {
    const active = currentSort.key === col.key;
    const indicator = active ? ` ${currentSort.direction === "asc" ? "▲" : "▼"}` : "";
    const label = active ? `${col.label}, sorted ${currentSort.direction === "asc" ? "ascending" : "descending"}` : `Sort by ${col.label}`;
    return `<th><button type="button" class="sort-button" data-sort="${esc(col.key)}" aria-pressed="${active ? "true" : "false"}" aria-label="${esc(label)}">${esc(col.label)}${indicator}</button></th>`;
  }).join("");
  const body = rows.map(row => `<tr>${currentTableColumns.map(col => `<td>${formatCell(row, col)}</td>`).join("")}</tr>`).join("");
  document.querySelector("#tables").innerHTML = `<div class="tablewrap"><table><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table></div>`;
  document.querySelectorAll(".sort-button").forEach(button => button.addEventListener("click", () => {
    const key = button.dataset.sort;
    currentSort = { key, direction: currentSort.key === key && currentSort.direction === "desc" ? "asc" : "desc" };
    renderSharedTable();
    updateURLState();
  }));
  document.querySelectorAll("[data-copy]").forEach(button => button.addEventListener("click", () => navigator.clipboard && navigator.clipboard.writeText(button.dataset.copy)));
  document.querySelectorAll("[data-request-id]").forEach(button => button.addEventListener("click", () => loadRequestDetail(button.dataset.requestId)));
}

function visibleRows() {
  const search = document.querySelector("#tableSearch").value.trim().toLowerCase();
  let rows = currentTableRows.slice();
  if (search) {
    rows = rows.filter(row => JSON.stringify(row).toLowerCase().includes(search));
  }
  if (currentSort.key) {
    const direction = currentSort.direction === "asc" ? 1 : -1;
    rows.sort((a, b) => compareValues(a[currentSort.key], b[currentSort.key]) * direction);
  }
  const limit = Number(document.querySelector("#pageSize").value) || 50;
  return rows.slice(0, limit);
}

function compareValues(a, b) {
  const na = Number(a);
  const nb = Number(b);
  if (Number.isFinite(na) && Number.isFinite(nb)) return na === nb ? 0 : na > nb ? 1 : -1;
  return String(a ?? "").localeCompare(String(b ?? ""));
}

function formatCell(row, col) {
  const value = row[col.key];
  const formatted = col.format ? col.format(value, row) : esc(value ?? "");
  if (col.copy && value) return `${formatted}<button type="button" class="copy-button" data-copy="${esc(value)}">Copy</button>`;
  if (col.request && value) return `<button type="button" class="copy-button" data-request-id="${esc(value)}">${esc(value)}</button>`;
  return formatted;
}

async function loadRequestDetail(requestID) {
  const res = await fetch(`api/request/${encodeURIComponent(requestID)}`, { credentials: "same-origin" });
  if (!res.ok) throw new Error(`request detail failed: ${res.status}`);
  const detail = await res.json();
  const panel = document.querySelector("#detailPanel");
  panel.hidden = false;
  panel.innerHTML = `<h2>Request ${esc(requestID)}</h2>${detailTable("Usage", detail.request)}${detailTable("Attempts", detail.attempts || [])}${detailTable("Trace", detail.trace || [])}${detailTable("Errors", detail.errors || [])}`;
}

function detailTable(title, data) {
  const rows = Array.isArray(data) ? data : [data];
  if (!rows.length || !rows[0]) return "";
  const keys = Object.keys(rows[0]);
  return `<h3>${esc(title)}</h3><div class="tablewrap"><table><thead><tr>${keys.map(k => `<th>${esc(k)}</th>`).join("")}</tr></thead><tbody>${rows.map(row => `<tr>${keys.map(k => `<td>${esc(row[k] ?? "")}</td>`).join("")}</tr>`).join("")}</tbody></table></div>`;
}

function populateBaselines(baselines, selected) {
  const select = document.querySelector("#baselineSelect");
  if (select.options.length && select.dataset.loaded === "true") {
    select.value = selected || select.value;
    return;
  }
  select.innerHTML = baselines.map(b => `<option value="${esc(b.baseline_id)}">${esc(b.baseline_name)}</option>`).join("") +
    `<option value="custom">Custom session baseline</option>`;
  select.dataset.loaded = "true";
  select.value = selected || "gpt-5.5";
}

function scalarColumns(options = {}) {
  const columns = [
    { key: "key", label: "Key", copy: true },
    { key: "secondaryKey", label: "Secondary", copy: true },
    { key: "requests", label: "Requests" },
    { key: "errors", label: "Errors" },
    { key: "errorRatePct", label: "Error %", format: v => formatUnit(v, "percent", true) },
    { key: "totalTokens", label: "Total Tokens" },
    { key: "inputTokens", label: "Input Tokens" },
    { key: "outputTokens", label: "Output Tokens" },
    { key: "inputCostUsd", label: "Input cost", format: v => usd.format(v || 0) },
    { key: "imageCostUsd", label: "Image cost", format: v => usd.format(v || 0) },
    { key: "outputCostUsd", label: "Output cost", format: v => usd.format(v || 0) },
    { key: "totalCostUsd", label: "Total cost", format: v => usd.format(v || 0) }
  ];
  if (options.includeSavings) {
    columns.push(
    { key: "baselineCostUsd", label: "Baseline", format: v => v == null ? "" : usd.format(v || 0) },
    { key: "savingsUsd", label: "Savings", format: v => v == null ? "" : usd.format(v || 0) },
      { key: "savingsPct", label: "Savings %", format: v => v == null ? "" : formatUnit(v, "percent", true) }
    );
  }
  columns.push(
    { key: "avgLatencyMs", label: "Avg latency", format: v => formatUnit(v, "ms", true) },
    { key: "avgUpstreamOutputTokensPerSec", label: "Upstream output tok/s", format: v => formatUnit(v, "tok/s", true) },
    { key: "avgUpstreamTotalTokensPerSec", label: "Upstream total tok/s", format: v => formatUnit(v, "tok/s", true) },
    { key: "avgDownstreamWriteOutputTokensPerSec", label: "Downstream write output tok/s", format: v => formatUnit(v, "tok/s", true) },
    { key: "avgDownstreamWriteTotalTokensPerSec", label: "Downstream write total tok/s", format: v => formatUnit(v, "tok/s", true) },
    { key: "cacheHits", label: "Cache hits" },
    { key: "cacheMisses", label: "Cache misses" },
    { key: "fallbacks", label: "Fallbacks" },
    { key: "inputImageCount", label: "Images" },
    { key: "piiFilteredRequests", label: "PII filtered" }
  );
  return columns;
}

function anomalyColumns() {
  return [
    { key: "key", label: "Signal", copy: true },
    { key: "secondaryKey", label: "Provider/model", copy: true },
    { key: "requests", label: "Requests" },
    { key: "errors", label: "Errors" },
    { key: "errorRatePct", label: "Error %", format: v => formatUnit(v, "percent", true) },
    { key: "attempts", label: "Attempts" },
    { key: "fallbacks", label: "Fallbacks" },
    { key: "fallbackRatePct", label: "Fallback %", format: v => formatUnit(v, "percent", true) },
    { key: "avgLatencyMs", label: "Avg latency", format: v => formatUnit(v, "ms", true) },
    { key: "maxLatencyMs", label: "Max latency", format: v => formatUnit(v, "ms", true) },
    { key: "totalCostUsd", label: "Total cost", format: v => usd.format(v || 0) },
    { key: "totalTokens", label: "Total Tokens" }
  ];
}

function aggregateColumns() {
  return [
    { key: "key", label: "Key", copy: true },
    { key: "requests", label: "Requests" },
    { key: "errors", label: "Errors" },
    { key: "totalTokens", label: "Total Tokens" },
    { key: "imageCostUsd", label: "Image cost", format: v => usd.format(v || 0) },
    { key: "totalCostUsd", label: "Total cost", format: v => usd.format(v || 0) },
    { key: "attempts", label: "Attempts" },
    { key: "fallbacks", label: "Fallbacks" },
    { key: "avgLatencyMs", label: "Avg latency", format: v => formatUnit(v, "ms", true) }
  ];
}

function savingsColumns() {
  return [
    { key: "key", label: "Model group", copy: true },
    { key: "requests", label: "Requests" },
    { key: "input_tokens", label: "Input Tokens" },
    { key: "output_tokens", label: "Output Tokens" },
    { key: "total_tokens", label: "Total Tokens" },
    { key: "actual_cost_usd", label: "Actual cost", format: v => usd.format(v || 0) },
    { key: "baseline_cost_usd", label: "Baseline cost", format: v => usd.format(v || 0) },
    { key: "savings_usd", label: "Savings", format: v => usd.format(v || 0) },
    { key: "savings_pct", label: "Savings %", format: v => formatUnit(v, "percent", true) }
  ];
}

function requestColumns() {
  return [
    { key: "timeUtc", label: "Time" },
    { key: "requestId", label: "Request", request: true },
    { key: "callerId", label: "Caller", copy: true },
    { key: "callerIp", label: "IP", copy: true },
    { key: "tokenId", label: "Key", copy: true },
    { key: "callerUser", label: "User" },
    { key: "project", label: "Project" },
    { key: "environment", label: "Env" },
    { key: "client", label: "Client" },
    { key: "requestedModel", label: "Requested" },
    { key: "modelGroup", label: "Group" },
    { key: "provider", label: "Provider" },
    { key: "model", label: "Model" },
    { key: "dialect", label: "Dialect" },
    { key: "status", label: "Status" },
    { key: "cache", label: "Cache" },
    { key: "attempts", label: "Attempts" },
    { key: "fallback", label: "Fallback" },
    { key: "latencyMs", label: "Latency", format: v => formatUnit(v, "ms", true) },
    { key: "totalTokens", label: "Total Tokens" },
    { key: "inputTokens", label: "Input Tokens" },
    { key: "outputTokens", label: "Output Tokens" },
    { key: "totalCostUsd", label: "Total cost", format: v => usd.format(v || 0) }
  ];
}

function catalogColumns() {
  return [
    { key: "source", label: "Source" },
    { key: "provider", label: "Provider", copy: true },
    { key: "modelRef", label: "Model ref", copy: true },
    { key: "model", label: "Model", copy: true },
    { key: "dialect", label: "Dialect" },
    { key: "activeGroups", label: "Active groups", format: v => esc((v || []).join(", ")) },
    { key: "activeTargetCount", label: "Targets" },
    { key: "groupTargetIndex", label: "Target index" },
    { key: "validationStatus", label: "Validation" },
    { key: "validationWorkload", label: "Workload" },
    { key: "validationAgeBucket", label: "Age" },
    { key: "validatedAt", label: "Validated" },
    { key: "qualityScore", label: "Quality", format: v => v ? fmt.format(v) : "" },
    { key: "passRate", label: "Pass rate", format: v => v ? formatUnit(v * 100, "percent", true) : "" },
    { key: "contextTokens", label: "Context" },
    { key: "inputModalities", label: "Input", format: v => esc((v || []).join(", ")) },
    { key: "outputModalities", label: "Output", format: v => esc((v || []).join(", ")) },
    { key: "toolSupport", label: "Tools", format: v => esc((v || []).join(", ")) },
    { key: "inputPricePerMillionUsd", label: "Input USD/M", format: v => v ? usd.format(v) : "" },
    { key: "outputPricePerMillionUsd", label: "Output USD/M", format: v => v ? usd.format(v) : "" },
    { key: "pricingSource", label: "Pricing source" },
    { key: "pricingUpdatedAt", label: "Pricing date" },
    { key: "pricingMissing", label: "Pricing missing" }
  ];
}

function retentionRows(report) {
  const job = report.latestJob ? [{ kind: "job", key: `job:${report.latestJob.jobId}`, ...report.latestJob }] : [];
  const tables = (report.tables || []).map(row => ({ kind: "retention-table", key: `${row.dataClass}:${row.tableName}`, ...row }));
  const rollups = (report.rollups || []).map(row => ({ kind: "rollup", key: `rollup:${row.runId}`, ...row }));
  return job.concat(tables, rollups);
}

function retentionColumns() {
  return [
    { key: "kind", label: "Kind" },
    { key: "key", label: "Key", copy: true },
    { key: "status", label: "Status" },
    { key: "mode", label: "Mode" },
    { key: "dryRun", label: "Dry run" },
    { key: "startedAt", label: "Started" },
    { key: "completedAt", label: "Completed" },
    { key: "dataClass", label: "Data class" },
    { key: "tableName", label: "Table" },
    { key: "cutoff", label: "Cutoff" },
    { key: "retentionDays", label: "Retention days" },
    { key: "candidateRows", label: "Candidates" },
    { key: "heldRows", label: "Held" },
    { key: "eligibleRows", label: "Eligible" },
    { key: "blockedRows", label: "Blocked" },
    { key: "deletedRows", label: "Deleted" },
    { key: "rollupType", label: "Rollup" },
    { key: "windowStart", label: "Window start" },
    { key: "windowEnd", label: "Window end" },
    { key: "sourceRequestCount", label: "Source requests" },
    { key: "rollupRows", label: "Rollup rows" },
    { key: "finalizedAt", label: "Finalized" },
    { key: "message", label: "Message" }
  ];
}

function securityColumns() {
  return [
    { key: "timeUtc", label: "Time" },
    { key: "eventType", label: "Event" },
    { key: "surface", label: "Surface" },
    { key: "outcome", label: "Outcome" },
    { key: "reason", label: "Reason" },
    { key: "status", label: "Status" },
    { key: "requestId", label: "Request", request: true },
    { key: "callerUser", label: "User" },
    { key: "project", label: "Project" },
    { key: "tokenId", label: "Key", copy: true },
    { key: "adminSubject", label: "Admin" },
    { key: "ipAddress", label: "IP", copy: true },
    { key: "ipSource", label: "IP source" },
    { key: "client", label: "Client" },
    { key: "inputTokens", label: "Input Tokens" },
    { key: "outputTokens", label: "Output Tokens" },
    { key: "totalTokens", label: "Total Tokens" }
  ];
}

function esc(value) {
  return String(value ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

document.querySelector("#themeToggle").addEventListener("click", () => {
  dismissChartTooltip();
  const next = currentTheme() === "dark" ? "light" : "dark";
  applyTheme(next, true);
  if (lastReport) renderCharts(lastReport);
  if (lastSavings) renderSavingsChartSpecs(lastSavings.charts || []);
});

document.querySelector("#filters").addEventListener("submit", event => {
  event.preventDefault();
  dismissChartTooltip();
  updateURLState();
  load().catch(err => document.querySelector("#tables").innerHTML = `<div class="error">${esc(err.message)}</div>`);
});
document.querySelectorAll(".tabs button").forEach(button => button.addEventListener("click", () => {
  dismissChartTooltip();
  document.querySelectorAll(".tabs button").forEach(b => b.classList.remove("active"));
  button.classList.add("active");
  activeTab = button.dataset.tab;
  currentSort = { key: "", direction: "desc" };
  updateURLState();
  load().catch(err => document.querySelector("#tables").innerHTML = `<div class="error">${esc(err.message)}</div>`);
}));
document.querySelector("#savingsFilters").addEventListener("submit", event => {
  event.preventDefault();
  dismissChartTooltip();
  updateURLState();
  loadSavings().catch(err => document.querySelector("#tables").innerHTML = `<div class="error">${esc(err.message)}</div>`);
});
document.querySelector("#tableSearch").addEventListener("input", () => {
  updateURLState();
  renderSharedTable();
});
document.querySelector("#pageSize").addEventListener("change", () => {
  updateURLState();
  if (genericTabs[activeTab]) loadGeneric(activeTab).catch(err => document.querySelector("#tables").innerHTML = `<div class="error">${esc(err.message)}</div>`);
  else renderSharedTable();
});
document.querySelector("#refreshReport").addEventListener("click", () => {
  dismissChartTooltip();
  load().catch(err => document.querySelector("#tables").innerHTML = `<div class="error">${esc(err.message)}</div>`);
});
document.querySelector("#copyLink").addEventListener("click", () => {
  updateURLState();
  if (navigator.clipboard) navigator.clipboard.writeText(window.location.href);
});
document.querySelector("#exportCsv").addEventListener("click", () => exportCSV());

applyTheme(storedTheme() || systemTheme(), false);
restoreURLState();
load().catch(err => document.querySelector("#tables").innerHTML = `<div class="error">${esc(err.message)}</div>`);

function exportCSV() {
  if (genericTabs[activeTab] && genericTabs[activeTab].security) {
    window.location.href = `security/export.csv?${qs()}`;
    return;
  }
  const rows = visibleRows();
  const columns = currentTableColumns || [];
  if (!rows.length || !columns.length) return;
  const csv = [columns.map(col => csvCell(col.label)).join(",")].concat(rows.map(row => columns.map(col => csvCell(row[col.key])).join(","))).join("\n");
  const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
  const link = document.createElement("a");
  link.href = URL.createObjectURL(blob);
  link.download = `admin-report-${activeTab}-${new Date().toISOString().replace(/[:.]/g, "-")}.csv`;
  link.click();
  URL.revokeObjectURL(link.href);
}

function csvCell(value) {
  const text = String(value ?? "");
  return /[",\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
}
