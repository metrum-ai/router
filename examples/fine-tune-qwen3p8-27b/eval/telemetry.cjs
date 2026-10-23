// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
// Allowlist ONLY safe router request IDs before promptfoo redacts HTTP headers.
// Never disable promptfoo's credential redaction or copy entire metadata/headers.
const fs = require('node:fs');
module.exports = async function(hookName, context) {
  if (hookName !== 'afterEach' || !process.env.FT_ROUTER_JOURNAL) return;
  const row = context.result;
  const headers = row?.response?.metadata?.http?.headers || row?.metadata?.http?.headers || {};
  const requestId = Object.entries(headers).find(([k]) => k.toLowerCase() === 'x-request-id')?.[1];
  if (typeof requestId !== 'string' || !/^req_[A-Za-z0-9_-]{1,100}$/.test(requestId)) {
    throw new Error('Missing safe router request ID; cannot join telemetry');
  }
  const provider = row.provider?.id;
  if (typeof provider !== 'string' || !provider.startsWith('openai:chat:')) {
    throw new Error('Unexpected provider identity');
  }
  fs.appendFileSync(process.env.FT_ROUTER_JOURNAL, JSON.stringify({
    provider, case_index: row.testIdx, router_request_id: requestId,
  }) + '\n', {mode: 0o600});
};
