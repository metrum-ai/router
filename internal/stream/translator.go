// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

// StreamTranslator converts upstream SSE events into caller-dialect events
// incrementally. Implementations are state machines: they may hold bounded
// per-item state (open tool-call ids, content-block indexes, emitted sequence
// numbers) but never the full response body.
type StreamTranslator interface {
	// Begin emits any preamble the caller dialect requires before the first
	// upstream event is seen. Anthropic needs message_start with input_tokens;
	// Responses needs response.created. est carries the reservation-time input
	// token estimate, used where the caller dialect demands usage up front.
	Begin(est TokenEstimate) ([]Event, error)

	// Next is called once per upstream event, in order. It returns zero or more
	// caller-dialect events. Returning ErrTerminal commits the translator to
	// emitting only Finish; Next is not called again.
	Next(up Event) ([]Event, error)

	// Finish emits the caller-dialect terminator and reconciles usage. Called
	// exactly once, on normal completion, caller cancel, upstream error, and
	// translator error. Must be safe to call after a partial stream.
	Finish(reason StopReason, usage Usage) ([]Event, error)
}
