// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
//
// serve-handler 6.1.7 calls minimatch as minimatch(path, pattern), which only
// exists in minimatch < 11. The patched line (11.x) exports an object instead,
// so this shim restores the callable surface while using the patched matcher.
// docs-site overrides minimatch to this package; minimatch-v11 is the pinned
// upstream dependency.
const upstream = require('minimatch-v11');

const match = upstream.minimatch || upstream;
module.exports = match;
module.exports.minimatch = match;
