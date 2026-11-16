// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

type Target = {
  provider: string;
  model: string;
  displayName?: string;
  tier?: string;
  weight: number;
  keyConfigured: boolean;
};

type RouteContext = {
  text: string;
  targets: Target[];
};

type Candidate = {
  target: Target;
  index: number;
};

const piiPatterns: RegExp[] = [
  /\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b/i,
  /\b(?:\+?1[-.\s]?)?(?:\(?\d{3}\)?[-.\s]?)\d{3}[-.\s]?\d{4}\b/,
  /\b\d{3}-\d{2}-\d{4}\b/,
  /\b(?:\d[ -]*?){13,19}\b/,
  /\b\d{1,5}\s+[A-Z0-9][A-Z0-9.'-]*(?:\s+[A-Z0-9][A-Z0-9.'-]*)*\s+(?:Street|St|Avenue|Ave|Road|Rd|Boulevard|Blvd|Drive|Dr|Lane|Ln|Court|Ct|Way|Place|Pl)\b/i,
  /\b[A-Z][a-z]+(?:\s+[A-Z][a-z]+){1,3}\b/,
  /\b(?:DOB|date of birth|birthdate)\s*[:=-]?\s*(?:\d{1,2}[/-]\d{1,2}[/-]\d{2,4}|[A-Z][a-z]+\s+\d{1,2},\s+\d{4})\b/i,
  /\b(?:MRN|medical record|patient id)\s*[:#-]?\s*[A-Z0-9-]{4,}\b/i,
  /\b(?:account|acct|routing)\s*(?:number|no\.?|#)?\s*[:#-]?\s*\d{6,17}\b/i,
  /\b(?:passport|driver'?s license|license)\s*(?:number|no\.?|#)?\s*[:#-]?\s*[A-Z0-9-]{5,}\b/i,
];

function eligibleTargets(ctx: RouteContext): Candidate[] {
  return ctx.targets
    .map((target, index) => ({ target, index }))
    .filter((entry) => entry.target.keyConfigured && entry.target.weight > 0);
}

function hasLikelyPII(text: string): boolean {
  return piiPatterns.some((pattern) => pattern.test(text));
}

function isSensitiveTarget(target: Target): boolean {
  return /^(sensitive|private)$/i.test(target.tier || "") ||
    /\b(sensitive|private)\b/i.test(target.displayName || "") ||
    /\b(sensitive|private)\b/i.test(target.model || "");
}

export function route(ctx: RouteContext) {
  const eligible = eligibleTargets(ctx);
  const fallback = eligible[0] || { index: 0 };

  if (hasLikelyPII(ctx.text || "")) {
    const sensitiveTargets = eligible.filter((entry) => isSensitiveTarget(entry.target));
    const sensitive = sensitiveTargets[0];
    if (!sensitive) {
      throw new Error("pii-detected:no-sensitive-target");
    }
    return {
      targetIndex: sensitive.index,
      fallbackIndexes: sensitiveTargets
        .filter((entry) => entry.index !== sensitive.index)
        .map((entry) => entry.index),
      classLabel: "pii-detected:sensitive-route",
    };
  }

  const normal = eligible.find((entry) => !isSensitiveTarget(entry.target)) || fallback;
  return {
    targetIndex: normal.index,
    fallbackIndexes: eligible
      .filter((entry) => entry.index !== normal.index)
      .map((entry) => entry.index),
    classLabel: "pii-detected:none",
  };
}
