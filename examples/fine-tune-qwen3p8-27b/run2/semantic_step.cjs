// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
// Infrastructure-only execution of frozen llm-rubric assertions on recorded strings.
// No evaluated-model calls, output normalization, fallback, retries or grade borrowing.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
process.umask(0o077);
const [input, output, mode, proof] = process.argv.slice(2);
if (!input || !output || !['proof','whole'].includes(mode)) throw Error('input output proof|whole required');
const items = JSON.parse(fs.readFileSync(input));
if (items.length !== 800) throw Error('whole 400 per merged arm required');
for (const arm of ['nvidia_merged','amd_merged']) {
  for (const stage of ['primary-326','sampling-326','sampling-327','sampling-328']) {
    if (items.filter(x => x.arm === arm && x.stage === stage).length !== 100) throw Error('cohort mismatch');
  }
}
if (mode === 'whole') {
  if (!proof) throw Error('successful independent grader proof required before batch');
  const row = JSON.parse(fs.readFileSync(proof, 'utf8').trim());
  const http = JSON.parse(fs.readFileSync(proof+'.http.jsonl', 'utf8').trim());
  if (http.status !== 200 || !http.request_id || !(row.grade.tokensUsed?.total > 0) || row.source_sha256 !== items[0].source_sha256) throw Error('grader proof invalid');
}
setTimeout(() => { console.error('infrastructure deadline, preserve partial step without retry'); process.exit(3); }, 15*60*1000).unref();
const journal = output + '.http.jsonl';
if (fs.existsSync(output) || fs.existsSync(journal)) throw Error('never overwrite an attempted step');
fs.writeFileSync(journal, '');
const nativeFetch = global.fetch;
global.fetch = async (url, options) => {
  if (!String(url).startsWith('http://127.0.0.1:18645/v1/')) throw Error('non-localhost grader destination');
  const body = JSON.parse(options.body);
  if (body.model !== 'frontier-ref' || body.temperature !== 0 || body.max_tokens !== 256) throw Error('grader parameters differ');
  const response = await nativeFetch(url, options);
  fs.appendFileSync(journal, JSON.stringify({status:response.status,request_id:response.headers.get('x-request-id'),body_sha256:crypto.createHash('sha256').update(options.body).digest('hex')})+'\n');
  return response;
};
const sdk = require(path.resolve(__dirname,'../.cache/promptfoo/node_modules/promptfoo'));
sdk.cache.disableCache();
(async () => {
  const rows = mode === 'proof' ? items.slice(0,1) : items;
  let next = 0, results = [];
  async function worker() {
    while (next < rows.length) {
      const item = rows[next++];
      if (typeof item.response.output !== 'string') throw Error('missing string remains infrastructure incomplete, no invented output');
      const test = structuredClone(item.testCase);
      if (test.assert.length !== 1 || test.assert[0].type !== 'llm-rubric' || test.assert[0].transform) throw Error('frozen single rubric only');
      const cfg = test.options.provider.config;
      if (test.options.provider.id !== 'openai:chat:frontier-ref' || cfg.temperature !== 0 || cfg.max_tokens !== 256 || cfg.maxRetries !== 0 || cfg.showThinking !== false) throw Error('frozen grader mismatch');
      cfg.apiBaseUrl = 'http://127.0.0.1:18645/v1'; cfg.apiKeyEnvar = 'ROUTER_CALLER_KEY';
      const grade = await sdk.assertions.runAssertion({prompt:item.prompt.raw,provider:{id:()=>item.provider.id},assertion:test.assert[0],test,vars:item.vars,providerResponse:item.response});
      const record = {arm:item.arm,stage:item.stage,testIdx:item.testIdx,source_sha256:item.source_sha256,grade};
      fs.appendFileSync(output,JSON.stringify(record)+'\n');results.push(record);
      console.log(item.arm,item.stage,item.testIdx,'persisted');
    }
  }
  await Promise.all(Array.from({length:mode === 'proof'?1:16},worker));
  if (results.length !== rows.length) throw Error('incomplete whole step');
})().catch(e=>{console.error(e);process.exitCode=1;});
