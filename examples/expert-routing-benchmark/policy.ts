// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// Small, interpretable text classifier for the preregistered public benchmark.
// It sees request text in the operator-owned router process; it never logs it.
// Caller/project metadata and benchmark labels are intentionally not inputs.

type Target = { provider: string; keyConfigured: boolean; weight: number };
type Context = { text?: string; targets: Target[] };
type Domain = 'biomedical' | 'finance' | 'math' | 'coding';

const VOCAB: Record<Domain, string[]> = {
  biomedical: ['gene', 'genes', 'protein', 'proteins', 'biomedical', 'genome', 'mutation', 'patient', 'clinical', 'diagnosis', 'treatment', 'disease', 'blood', 'nerve', 'syndrome'],
  finance: ['finance', 'financial', 'revenue', 'earnings', 'investment', 'stock', 'stocks', 'securities', '재무', '증권'],
  math: ['math', 'calculate', 'sum', 'numbers', 'number', 'total', 'multiply', 'divide', 'arithmetic'],
  coding: ['def', 'function', 'python', 'code', 'programming', 'algorithm', 'implement'],
};

export function classify(text: string): Domain | 'unknown' {
  const lower = text.toLowerCase();
  const scores = Object.entries(VOCAB).map(([domain, words]) => ({
    domain: domain as Domain, score: words.reduce((sum, word) => {
      const match = /^[a-z]+$/.test(word) ? new RegExp(`\\b${word}\\b`).test(lower) : lower.includes(word);
      return sum + (match ? 1 : 0);
    }, 0),
  })).sort((a, b) => b.score - a.score);
  // Ambiguous and unseen inputs use the fixed fallback, not a guess.
  if (!scores[0].score || scores[0].score === scores[1].score) return 'unknown';
  return scores[0].domain;
}

export function route(ctx: Context) {
  const domain = classify(ctx.text || '');
  // The coding expert is an explicit out-of-domain fallback; it is NOT the
  // separate tensor-parallel base model and is not a claim of general quality.
  const provider = domain === 'unknown' ? 'coding' : domain;
  const selected = ctx.targets.findIndex(t => t.provider === provider && t.keyConfigured && t.weight > 0);
  if (selected < 0) throw new Error('required expert unavailable');
  // Never silently retry another domain after a target failure.
  return { targetIndex: selected, fallbackIndexes: [], classLabel: `domain:${domain}` };
}
