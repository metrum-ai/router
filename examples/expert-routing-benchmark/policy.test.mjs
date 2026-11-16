// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { classify, route } from './policy.ts';

const targets = ['biomedical', 'finance', 'math', 'coding'].map(provider => ({provider, weight: 1, keyConfigured: true}));

test('classifies content without looking at caller persona', () => {
  for (const [text, want] of [['Find the gene and protein', 'biomedical'], ['Find financial revenue', 'finance'],
    ['Calculate the arithmetic sum', 'math'], ['Implement a Python function', 'coding']]) {
    assert.equal(classify(text), want);
    const result = route({text, targets, caller: {project: 'wrong-label'}});
    assert.equal(targets[result.targetIndex].provider, want);
    assert.equal(result.classLabel, `domain:${want}`);
    assert.deepEqual(result.fallbackIndexes, []);
  }
});

test('unseen and tied domains use fixed coding fallback', () => {
  for (const text of ['What is the weather?', 'Explain a gene and a math problem.', 'A general zipcode.']) {
    const result = route({text, targets});
    assert.equal(result.classLabel, 'domain:unknown');
    assert.equal(targets[result.targetIndex].provider, 'coding');
  }
});

test('unavailable primary fails closed rather than rerouting silently', () => {
  const disabled = targets.map(t => t.provider === 'finance' ? {...t, keyConfigured: false} : t);
  assert.throws(() => route({text: 'financial revenue', targets: disabled}), /unavailable/);
});
