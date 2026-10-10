// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// Standalone-versus-embedded host detection for Metrum AI Router docs.
// Standalone hosted docs (docs.metrum.ai and the temporary origin) keep the
// <router-host> placeholder; docs embedded in a live router under /docs/ use
// the browser origin. Origins come from docs-site/docs-origin.json.

function hostnameOf(origin) {
  try {
    return new URL(origin).hostname.toLowerCase();
  } catch {
    return "";
  }
}

export function standaloneDocsHostnames(docsOrigin, extraOrigins = []) {
  const origins = [
    docsOrigin?.canonicalOrigin,
    docsOrigin?.permanentOrigin,
    docsOrigin?.temporaryOrigin,
    ...extraOrigins,
  ];
  return [...new Set(origins.filter(Boolean).map(hostnameOf).filter(Boolean))];
}

export function isEmbeddedRouterDocsLocation(location, docsOrigin, extraOrigins = []) {
  if (!location) {
    return false;
  }
  const { hostname, pathname } = location;
  if (
    !hostname ||
    standaloneDocsHostnames(docsOrigin, extraOrigins).includes(hostname.toLowerCase())
  ) {
    return false;
  }
  const base = (docsOrigin?.baseUrl || "/docs/").replace(/\/+$/, "");
  return pathname === base || pathname.startsWith(`${base}/`);
}
