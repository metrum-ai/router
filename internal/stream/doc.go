// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// Package stream defines incremental upstream→caller SSE translation.
//
// # StreamTranslator contract
//
// A StreamTranslator is a per-direction state machine. It may hold bounded
// per-item state (open tool-call ids, content-block indexes, emitted sequence
// numbers) but never the full response body, and it must not look ahead beyond
// the single upstream Event passed to Next.
//
// Lifecycle:
//
//  1. Begin(est) may construct caller preamble events (for example Anthropic
//     message_start with reservation input_tokens, or Responses
//     response.created). Those events must not be written to the caller until
//     the upstream HTTP status is 2xx and the body is being read as SSE.
//     Upstream non-2xx before the first event still allows target fallback.
//     The first flushed caller frame is the commit point: after that, no
//     fallback to another target.
//
//  2. Next(up) is invoked once per upstream event, in order. It returns zero or
//     more caller-dialect events. Deltas are emitted immediately; only
//     dialect-terminal fields (finish_reason, message_delta.stop_reason,
//     response.completed, usage) may be deferred. Returning ErrTerminal
//     commits the translator to emitting only Finish; Next is not called
//     again.
//
//  3. Finish(reason, usage) runs exactly once — on normal completion, caller
//     cancel, upstream error, or translator error — and must be safe after a
//     partial stream. It emits the caller-dialect terminator and reconciles
//     usage.
//
// Unknown upstream event types are dropped and counted; they are never
// forwarded raw. Panic in Next or Finish must be recovered by the runtime
// (F-006). Telemetry is scalar buckets only: never prompts, tool args, headers,
// or raw bodies.
package stream
