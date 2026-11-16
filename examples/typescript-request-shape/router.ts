// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// Deployment-owned TypeScript example: route by safe request-shape signals.
// This is not built-in router state. Callers still request one allowed group;
// the script only chooses among that group's eligible targets.

type Target = {
  provider: string;
  model: string;
  tier?: string;
  weight: number;
  keyConfigured: boolean;
};

type RouteContext = {
  text: string;
  targets: Target[];
  context?: {
    textChars?: number;
    estimatedTokens?: number;
    imageCount?: number;
    toolCount?: number;
    hasTools?: boolean;
    hasStructuredOutput?: boolean;
    stream?: boolean;
    reasoning?: {
      requested?: boolean;
      kind?: string;
      effort?: string;
    };
  };
};

type Candidate = {
  target: Target;
  index: number;
};

function eligibleTargets(ctx: RouteContext): Candidate[] {
  return ctx.targets
    .map((target, index) => ({ target, index }))
    .filter((entry) => entry.target.keyConfigured && entry.target.weight > 0);
}

function pickByTier(eligible: Candidate[], preferredTier: string[]): Candidate | undefined {
  for (const tier of preferredTier) {
    const match = eligible.find((entry) => (entry.target.tier || "").toLowerCase() === tier);
    if (match) {
      return match;
    }
  }
  return undefined;
}

function classify(ctx: RouteContext): { shape: string; preferredTiers: string[] } {
  const summary = ctx.context || {};
  const textChars = Number(summary.textChars || (ctx.text || "").length || 0);
  const imageCount = Number(summary.imageCount || 0);
  const toolCount = Number(summary.toolCount || 0);
  const hasTools = Boolean(summary.hasTools || toolCount > 0);
  const hasStructured = Boolean(summary.hasStructuredOutput);
  const reasoningRequested = Boolean(summary.reasoning && summary.reasoning.requested);

  if (imageCount > 0) {
    return { shape: "image", preferredTiers: ["vision", "multimodal", "heavy"] };
  }
  if (reasoningRequested) {
    return { shape: "reasoning", preferredTiers: ["reasoning", "heavy"] };
  }
  if (hasTools) {
    return { shape: "tools", preferredTiers: ["tool", "heavy", "coding"] };
  }
  if (hasStructured) {
    return { shape: "structured", preferredTiers: ["structured", "heavy", "cheap"] };
  }
  if (textChars > 8000) {
    return { shape: "long-context", preferredTiers: ["heavy", "long_context"] };
  }
  return { shape: "chat", preferredTiers: ["cheap", "compact", "normal"] };
}

export function route(ctx: RouteContext) {
  const eligible = eligibleTargets(ctx);
  if (eligible.length === 0) {
    return { targetIndex: 0, classLabel: "request-shape:no-eligible-targets" };
  }

  const decision = classify(ctx);
  const preferred = pickByTier(eligible, decision.preferredTiers) || eligible[0];
  return {
    targetIndex: preferred.index,
    fallbackIndexes: eligible
      .filter((entry) => entry.index !== preferred.index)
      .map((entry) => entry.index),
    classLabel: `request-shape:${decision.shape}`,
  };
}
