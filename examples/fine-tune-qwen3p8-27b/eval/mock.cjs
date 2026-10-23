// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
module.exports = class MockProvider {
  id() { return 'metrum-synthetic-tool-provider'; }
  async callApi(prompt, context) {
    return { output: JSON.stringify(context.vars.output) };
  }
};
