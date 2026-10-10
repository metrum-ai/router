// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import React, { useEffect, useState } from "react";
import useDocusaurusContext from "@docusaurus/useDocusaurusContext";
import docsOrigin from "@site/docs-origin.json";
import { isEmbeddedRouterDocsLocation } from "./docsHost.mjs";
import styles from "./RouterEndpoint.module.css";

const FALLBACK_ORIGIN = "https://<router-host>";

function isLikelyEmbeddedRouterDocs(siteUrl) {
  if (typeof window === "undefined" || !window.location) {
    return false;
  }
  // Standalone hosted docs (temporary origin, docs.metrum.ai, or a DOCS_SITE_URL
  // build) keep the placeholder. Embedded router docs are served under /docs/
  // from the customer router origin.
  return isEmbeddedRouterDocsLocation(window.location, docsOrigin, [siteUrl]);
}

function useRouterOrigin() {
  const { siteConfig } = useDocusaurusContext();
  const [origin, setOrigin] = useState(FALLBACK_ORIGIN);

  useEffect(() => {
    if (isLikelyEmbeddedRouterDocs(siteConfig.url) && window.location?.origin) {
      setOrigin(window.location.origin);
    }
  }, [siteConfig.url]);

  return origin;
}

export function RouterOrigin() {
  return <code>{useRouterOrigin()}</code>;
}

export function RouterApiBase() {
  return <code>{useRouterOrigin()}/v1</code>;
}

export function DeploymentSpecificNote() {
  const origin = useRouterOrigin();
  const embedded = origin !== FALLBACK_ORIGIN;
  return (
    <div className="contactBanner">
      <p>
        {embedded ? (
          <>
            These docs are built into the Metrum AI Router server delivered for your
            deployment. Examples that show the router base URL use this browser origin, so
            on this deployment they render as <RouterOrigin /> and <RouterApiBase />.
          </>
        ) : (
          <>
            Replace <code>{FALLBACK_ORIGIN}</code> with your deployment URL. When these docs
            are served from a live router under <code>/docs/</code>, examples automatically
            use that router origin.
          </>
        )}
      </p>
    </div>
  );
}

export function RouterCodeBlock({ children, language = "bash" }) {
  const origin = useRouterOrigin();
  const text = String(children)
    .replaceAll("{{origin}}", origin)
    .replaceAll("{{apiBase}}", `${origin}/v1`);

  return (
    <pre className={styles.codeBlock}>
      <code className={`language-${language}`}>{text.trim()}</code>
    </pre>
  );
}
