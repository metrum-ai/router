// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useMemo, useState } from "react";
import type { ReportResponse, ReportRow } from "@/lib/reports";
import { Button } from "@/components/ui/button";

type SecurityEvent = ReportRow & { timeUtc?: string; callerId?: string; tokenId?: string; requestId?: string; reason?: string; eventType?: string; outcome?: string; path?: string; adminSubject?: string; requestedModel?: string };

function detectorParts(reason: string, detectorID?: string) {
  const parts = reason.split(":");
  if (parts[0] !== "detector_signal") return undefined;
  return { severity: parts[1] || "unknown", detector: detectorID || parts[3] || "unknown" };
}

function incidentKind(event: SecurityEvent): string | undefined {
  switch (String(event.eventType || "")) {
    case "security_detector_signal": return "Detector signal";
    case "security_detector_failure": return "Detector failure";
    case "api_auth_failed": return "Authentication failure";
    case "api_authz_failed": return "Authorization failure";
    case "admin_access":
      return String(event.path || "").includes("/containment") ? "Containment access" : undefined;
    default: return undefined;
  }
}

function eventSeverity(event: SecurityEvent): number {
  const detail = detectorParts(String(event.reason || ""), event.requestedModel);
  if (detail) return ({ critical: 4, high: 3, medium: 2, low: 1 } as Record<string, number>)[detail.severity.toLowerCase()] || 0;
  if (event.eventType === "api_auth_failed") return 3;
  if (event.eventType === "api_authz_failed") return 2;
  return 0;
}

function shellQuote(value: string) { return `'${value.replaceAll("'", "'\\''")}'`; }

export function SecurityConsole({ report }: { report?: ReportResponse }) {
  const [busyCaller, setBusyCaller] = useState("");
  const [message, setMessage] = useState("");
  const [containment, setContainment] = useState<Record<string, { can_contain: boolean; contained: boolean }>>({});
  const events = useMemo(() => (report?.rows || []) as SecurityEvent[], [report]);
  const incidents = useMemo(() => events.filter((event) => incidentKind(event)), [events]);
  const riskEvents = useMemo(() => events.filter((event) => event.eventType === "security_detector_signal" || event.eventType === "api_auth_failed" || event.eventType === "api_authz_failed"), [events]);
  const risks = useMemo(() => {
    const byKey = new Map<string, { key: string; callerId: string; count: number; severity: number; sources: Set<string> }>();
    for (const event of riskEvents) {
      const callerId = String(event.callerId || "");
      const tokenId = String(event.tokenId || "");
      const key = callerId || tokenId;
      if (!key) continue;
      const item = byKey.get(key) || { key: callerId || `Key ${tokenId}`, callerId, count: 0, severity: 0, sources: new Set<string>() };
      const detail = detectorParts(String(event.reason || ""), event.requestedModel);
      item.count++;
      item.severity = Math.max(item.severity, eventSeverity(event));
      if (detail) item.sources.add(detail.detector);
      else if (event.eventType === "api_auth_failed") item.sources.add("authentication");
      else if (event.eventType === "api_authz_failed") item.sources.add("authorization");
      byKey.set(key, item);
    }
    return [...byKey.values()].sort((a, b) => b.severity - a.severity || b.count - a.count);
  }, [riskEvents]);
  useEffect(() => {
    const callerIDs = [...new Set(risks.map((risk) => risk.callerId).filter(Boolean))].slice(0, 100);
    let active = true;
    const load = async () => {
      const entries: Array<readonly [string, { can_contain: boolean; contained: boolean }]> = [];
      for (let index = 0; index < callerIDs.length; index += 10) {
        const batch = await Promise.all(callerIDs.slice(index, index + 10).map(async (callerId) => {
          try {
            const response = await fetch(`api/security/containment-status?caller_id=${encodeURIComponent(callerId)}`, { credentials: "same-origin", headers: { Accept: "application/json" } });
            if (!response.ok) return [callerId, { can_contain: false, contained: false }] as const;
            return [callerId, await response.json() as { can_contain: boolean; contained: boolean }] as const;
          } catch { return [callerId, { can_contain: false, contained: false }] as const; }
        }));
        entries.push(...batch);
      }
      if (active) setContainment(Object.fromEntries(entries));
    };
    void load();
    return () => { active = false; };
  }, [risks]);

  async function contain(caller: string) {
    if (!window.confirm(`Contain caller ${caller}?`)) return;
    const reason = window.prompt("Enter an audit reason (1–64 letters, digits, dots, underscores, or hyphens):")?.trim() || "";
    if (!/^[a-zA-Z0-9._-]{1,64}$/.test(reason)) { setMessage("Action cancelled: reason must use 1–64 letters, digits, dots, underscores, or hyphens."); return; }
    setBusyCaller(caller); setMessage("");
    try {
      const csrf = await fetch("api/security/csrf-token", { credentials: "same-origin", headers: { Accept: "application/json" } });
      if (!csrf.ok) throw new Error(`Could not start a protected containment request (HTTP ${csrf.status}).`);
      const { csrf_token: csrfToken } = await csrf.json() as { csrf_token: string };
      const response = await fetch("api/security/containment", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", Accept: "application/json", "X-CSRF-Token": csrfToken }, body: JSON.stringify({ caller_id: caller, action: "suspend", reason }) });
      if (!response.ok) throw new Error(`Containment request failed (HTTP ${response.status}).`);
      setContainment((state) => ({ ...state, [caller]: { can_contain: true, contained: true } }));
      setMessage(`Caller ${caller}: contained and audited.`);
    } catch (error) { setMessage(error instanceof Error ? error.message : "Containment request failed."); }
    finally { setBusyCaller(""); }
  }

  async function exportBundle(event: SecurityEvent, format: "json" | "markdown") {
    try {
      const params = new URLSearchParams({ request_id: String(event.requestId || "") });
      if (report?.period?.from) params.set("from", report.period.from);
      if (report?.period?.to) params.set("to", report.period.to);
      const endpoint = format === "json" ? "security/export.csv" : "security/export.md";
      if (format === "json") params.set("format", "json");
      const response = await fetch(`${endpoint}?${params}`, { credentials: "same-origin", headers: { Accept: format === "json" ? "application/json" : "text/markdown" } });
      if (!response.ok) throw new Error(`Incident export failed (HTTP ${response.status}).`);
      const url = URL.createObjectURL(await response.blob());
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = `security-incident.${format === "json" ? "json" : "md"}`;
      anchor.click();
      URL.revokeObjectURL(url);
      setMessage("Incident bundle downloaded.");
    } catch (error) { setMessage(error instanceof Error ? error.message : "Incident export failed."); }
  }

  return <section className="space-y-4 rounded-lg border border-metrum-red/30 bg-white/[0.035] p-4" aria-label="Security console">
    <div><h3 className="font-display text-xl text-white">Key risk</h3><p className="text-sm text-white/60">Ranked by recent detector results and authentication failures. Scores summarize observed events; they are not a block decision.</p></div>
    {message ? <p role="status" className="text-sm text-white/75">{message}</p> : null}
    {risks.length ? <div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead><tr className="text-white/55"><th className="p-2">Caller / key</th><th className="p-2">Events</th><th className="p-2">Highest severity</th><th className="p-2">Top signals</th><th className="p-2">Action</th></tr></thead><tbody>{risks.map((risk) => { const state = containment[risk.callerId]; const reason = "operator-reviewed"; const body = JSON.stringify({ caller_id: risk.callerId, action: "resume", reason }); const releaseCall = `routerctl release --caller-id ${shellQuote(risk.callerId)} --reason ${shellQuote(reason)}\n\nPOST "$ROUTER_ADMIN_URL/admin/v1/containment" \\\n  -H "Authorization: Bearer $ROUTER_ADMIN_TOKEN" \\\n  -H "Content-Type: application/json" \\\n  --data ${shellQuote(body)}`; return <tr key={risk.key} className="border-t border-white/10"><td className="p-2">{risk.key}</td><td className="p-2">{risk.count}</td><td className="p-2">{["none", "low", "medium", "high", "critical"][risk.severity]}</td><td className="p-2">{[...risk.sources].slice(0, 3).join(", ") || "Unavailable"}</td><td className="p-2">{state?.can_contain && !state.contained ? <Button type="button" disabled={!!busyCaller} onClick={() => void contain(risk.callerId)}>Contain</Button> : null}{state?.contained ? <div className="space-y-1"><p>Contained. Release from the admin control plane:</p><code className="block whitespace-pre-wrap text-xs">{releaseCall}</code><Button type="button" variant="outline" onClick={() => void navigator.clipboard.writeText(releaseCall)}>Copy release instructions</Button></div> : null}</td></tr>; })}</tbody></table></div> : <p className="text-sm text-white/60">No detector or authentication signals in this report page. Adjust the time range or page through Security events.</p>}
    <div><h3 className="font-display text-xl text-white">Incident timeline</h3>{incidents.length ? <ol className="mt-2 space-y-2">{incidents.map((event, index) => { const kind = incidentKind(event) || "Security event"; return <li key={`${event.requestId}-${event.eventType}-${index}`} className="flex flex-wrap items-center justify-between gap-2 rounded border border-white/10 p-3 text-sm"><span>{event.timeUtc || "Unknown time"} · {kind} · {event.callerId || event.tokenId || event.adminSubject || "Unknown actor"} · {event.reason || event.path || event.outcome || event.eventType}</span><span className="flex gap-2">{event.requestId ? <a className="underline" href={`?tab=requests&request_id=${encodeURIComponent(event.requestId)}`}>Request evidence</a> : null}<Button type="button" variant="outline" disabled={!event.requestId} onClick={() => void exportBundle(event, "json")}>JSON</Button><Button type="button" variant="outline" disabled={!event.requestId} onClick={() => void exportBundle(event, "markdown")}>Markdown</Button></span></li>; })}</ol> : <p className="mt-2 text-sm text-white/60">No incidents on this page.</p>}</div>
  </section>;
}
