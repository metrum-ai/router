// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
// Synthetic Azure auth-code compatibility test. The injected client never makes network calls.
const assert = require('node:assert/strict');
const path = require('node:path');
const {createRequire} = require('node:module');
const base = process.env.EVAL_NODE_MODULES || path.join(__dirname, 'node_modules');
const local = createRequire(path.join(base, '../package.json'));
const {AuthorizationCodeCredential} = local('@azure/identity');
const {createHttpHeaders} = local('@azure/core-rest-pipeline');
const msal = createRequire(local.resolve('@azure/identity'))('@azure/msal-node');

// The patched confidential client rejects the unsupported instance-aware mode.
assert.throws(() => new msal.ConfidentialClientApplication({
  auth: {clientId: 'synthetic-client', clientSecret: 'synthetic-secret', instanceAware: true},
}), error => error.errorCode === 'unsupported_cca_instance_aware');

const origin = 'https://auth.invalid';
const tenant = 'test-tenant';
const requests = [];
const metadata = {
  issuer: `${origin}/${tenant}/v2.0`,
  authorization_endpoint: `${origin}/${tenant}/oauth2/v2.0/authorize`,
  token_endpoint: `${origin}/${tenant}/oauth2/v2.0/token`,
  jwks_uri: `${origin}/${tenant}/discovery/v2.0/keys`,
  end_session_endpoint: `${origin}/${tenant}/oauth2/v2.0/logout`,
};
const httpClient = {
  async sendRequest(request) {
    const url = new URL(request.url);
    assert.equal(url.origin, origin, 'unexpected authority');
    requests.push([request.method, url.pathname]);
    let body;
    if (request.method === 'GET' && url.pathname.endsWith('/.well-known/openid-configuration')) {
      body = metadata;
    } else if (request.method === 'POST' && url.pathname.endsWith('/oauth2/v2.0/token')) {
      const fields = new URLSearchParams(request.body);
      assert.equal(fields.get('grant_type'), 'authorization_code');
      assert.equal(fields.get('code'), 'synthetic-auth-code');
      body = {access_token: 'synthetic-access-token', token_type: 'Bearer', expires_in: 3600,
        scope: 'https://resource.invalid/.default'};
    } else {
      throw new Error(`unexpected mock request: ${request.method} ${url.pathname}`);
    }
    return {request, status: 200, headers: createHttpHeaders({'content-type': 'application/json'}),
      bodyAsText: JSON.stringify(body)};
  },
};
async function main() {
  const options = {authorityHost: origin, disableInstanceDiscovery: true, httpClient};
  const redirect = 'https://app.invalid/callback';
  const publicClient = new AuthorizationCodeCredential(tenant, 'synthetic-client',
    'synthetic-auth-code', redirect, options);
  const confidential = new AuthorizationCodeCredential(tenant, 'synthetic-client',
    'synthetic-secret', 'synthetic-auth-code', redirect, options);
  for (const credential of [publicClient, confidential]) {
    const token = await credential.getToken('https://resource.invalid/.default');
    assert.equal(token.token, 'synthetic-access-token');
  }
  assert.equal(requests.filter(([method]) => method === 'POST').length, 2);
  console.log('MSAL override: synthetic public and confidential auth-code flows passed');
}
main().catch(error => { console.error(error); process.exitCode = 1; });
