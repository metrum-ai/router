// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// Same group, explicit key AND project binding. Other callers retain teacher.
// No fallback to the canary for unrelated callers. Caller quotas remain active.
export function route(ctx: any) {
  const canary = ctx.caller && ctx.caller.project === "acme-support" &&
    ctx.caller.tokenId === "rtr_acme-support";
  const provider = canary ? "ft-canary" : "teacher";
  const index = ctx.targets.findIndex((target: any) => target.provider === provider);
  if (index < 0) throw new Error("required bound target unavailable");
  return {targetIndex: index, fallbackIndexes: [], classLabel: canary ? "support-key-canary" : "teacher"};
}
