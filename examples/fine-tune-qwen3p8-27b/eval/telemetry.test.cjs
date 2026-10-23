// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const hook = require('./telemetry.cjs');

test('journals only safe scalar request IDs, never whole headers', async () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'ft-journal-'));
  const old = process.env.FT_ROUTER_JOURNAL;
  try {
    const journal = path.join(directory, 'ids.jsonl');
    process.env.FT_ROUTER_JOURNAL = journal;
    await hook('afterEach', {result: {provider: {id: 'openai:chat:base'}, testIdx: 7,
      response: {metadata: {http: {headers: {'X-Request-Id': 'req_synthetic_123',
        Authorization: 'synthetic-not-a-credential', 'unexpected': 'private-prompt-marker'}}}}}});
    const content = fs.readFileSync(journal, 'utf8');
    assert.deepEqual(JSON.parse(content), {provider:'openai:chat:base',case_index:7,router_request_id:'req_synthetic_123'});
    assert.ok(!content.includes('Authorization'));
    assert.ok(!content.includes('private-prompt-marker'));
    assert.equal(fs.statSync(journal).mode & 0o777, 0o600);
  } finally {
    if (old === undefined) delete process.env.FT_ROUTER_JOURNAL;
    else process.env.FT_ROUTER_JOURNAL = old;
    fs.rmSync(directory,{recursive:true,force:true});
  }
});

test('rejects missing/redacted request IDs without disabling redaction', async () => {
  const old = process.env.FT_ROUTER_JOURNAL;
  process.env.FT_ROUTER_JOURNAL = '/unused';
  try {
    await assert.rejects(hook('afterEach',{result:{response:{metadata:{http:{headers:{'x-request-id':'[REDACTED]'}}}}}}), /Missing safe/);
  } finally {
    if (old === undefined) delete process.env.FT_ROUTER_JOURNAL;
    else process.env.FT_ROUTER_JOURNAL = old;
  }
});
