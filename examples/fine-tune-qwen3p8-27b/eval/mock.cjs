// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
module.exports = class MockProvider {
  id() { return 'metrum-synthetic-tool-provider'; }
  async callApi(prompt, context) {
    if (!Array.isArray(context.vars.declared)) throw new Error('declared tools must remain an atomic array');
    return { output: JSON.stringify(context.vars.output) };
  }
};
