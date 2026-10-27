// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
//
// Official TypeScript SDK API-compat coverage (metrum-ai/router#271, #94
// DoD 2): the official OpenAI and Anthropic TypeScript clients complete the
// supported unary/stream/tool-loop cases through a built router, and the
// returned objects are consumable with normal SDK helpers. The router is
// provisioned by run_suite.py against the scripted fake upstream, so this
// suite is offline and deterministic.

import { test } from "node:test";
import assert from "node:assert/strict";
import OpenAI from "openai";
import Anthropic from "@anthropic-ai/sdk";

const BASE = process.env.ROUTER_BASE_URL;
const TOKEN = process.env.ROUTER_CALLER_TOKEN;
if (!BASE || !TOKEN) {
  throw new Error("ROUTER_BASE_URL and ROUTER_CALLER_TOKEN are required (run via run_suite.py)");
}

const openai = new OpenAI({ baseURL: `${BASE}/v1`, apiKey: TOKEN });
const anthropic = new Anthropic({ baseURL: BASE, apiKey: TOKEN });

const TOOL = {
  type: "function",
  function: {
    name: "lookup",
    description: "Look up a value",
    parameters: { type: "object", properties: {}, additionalProperties: false },
  },
};

test("openai chat unary is consumable via SDK helpers", async () => {
  const completion = await openai.chat.completions.create({
    model: "chat",
    messages: [{ role: "user", content: "hi" }],
  });
  assert.equal(completion.object, "chat.completion");
  const choice = completion.choices[0];
  assert.equal(choice.finish_reason, "stop");
  assert.equal(choice.message.role, "assistant");
  assert.equal(typeof choice.message.content, "string");
  assert.ok(completion.usage.total_tokens > 0);
});

test("openai chat streaming yields incremental SDK chunks", async () => {
  const stream = await openai.chat.completions.create({
    model: "chat",
    stream: true,
    messages: [{ role: "user", content: "hi" }],
  });
  let deltas = 0;
  let sawUsage = false;
  for await (const chunk of stream) {
    deltas += 1;
    assert.equal(chunk.object, "chat.completion.chunk");
    if (chunk.usage && chunk.usage.total_tokens > 0) sawUsage = true;
  }
  assert.ok(deltas >= 2, `expected incremental chunks, got ${deltas}`);
  assert.equal(sawUsage, true, "expected the usage chunk");
});

test("openai chat tool loop: tool_calls then tool-result continuation", async () => {
  const messages = [{ role: "user", content: "look it up" }];
  const first = await openai.chat.completions.create({ model: "chat", messages, tools: [TOOL] });
  const call = first.choices[0].message.tool_calls?.[0];
  assert.ok(call, "first turn must return a tool call");
  assert.equal(call.function.name, "lookup");
  assert.equal(first.choices[0].finish_reason, "tool_calls");

  messages.push(first.choices[0].message);
  messages.push({ role: "tool", tool_call_id: call.id, content: "result-42" });
  const second = await openai.chat.completions.create({ model: "chat", messages });
  assert.equal(second.choices[0].finish_reason, "stop");
  assert.equal(typeof second.choices[0].message.content, "string");
});

test("openai responses unary", async () => {
  const response = await openai.responses.create({
    model: "responses",
    input: "hi",
  });
  assert.equal(response.status, "completed");
  assert.ok(response.output_text.length > 0);
});

test("anthropic messages unary is consumable via SDK helpers", async () => {
  const message = await anthropic.messages.create({
    model: "messages",
    max_tokens: 128,
    messages: [{ role: "user", content: "hi" }],
  });
  assert.equal(message.type, "message");
  const text = message.content.filter((b) => b.type === "text").map((b) => b.text).join("");
  assert.ok(text.length > 0);
  assert.ok(message.usage.input_tokens > 0);
});

test("anthropic messages streaming yields SDK events", async () => {
  const stream = anthropic.messages.stream({
    model: "messages",
    max_tokens: 128,
    messages: [{ role: "user", content: "hi" }],
  });
  let sawDelta = false;
  for await (const event of stream) {
    if (event.type === "content_block_delta") sawDelta = true;
  }
  const final = await stream.finalMessage();
  assert.equal(sawDelta, true, "expected content_block_delta events");
  assert.equal(final.type, "message");
});

test("anthropic tool loop: tool_use then tool_result continuation", async () => {
  const tool = {
    name: "lookup",
    description: "Look up a value",
    input_schema: { type: "object", properties: {} },
  };
  const messages = [{ role: "user", content: "look it up" }];
  const first = await anthropic.messages.create({ model: "messages", max_tokens: 128, messages, tools: [tool] });
  const use = first.content.find((b) => b.type === "tool_use");
  assert.ok(use, "first turn must return a tool_use block");
  assert.equal(use.name, "lookup");

  messages.push({ role: "assistant", content: first.content });
  messages.push({ role: "user", content: [{ type: "tool_result", tool_use_id: use.id, content: "result-42" }] });
  const second = await anthropic.messages.create({ model: "messages", max_tokens: 128, messages });
  const text = second.content.filter((b) => b.type === "text").map((b) => b.text).join("");
  assert.ok(text.length > 0);
});
