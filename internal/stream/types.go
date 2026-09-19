// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import "errors"

// Event is one SSE frame exchanged by StreamTranslator (upstream or caller).
//
// Framing fields not used by the caller protocol (comments, retry, id) are omitted.
type Event struct {
	// Name is the SSE "event:" value when present (Anthropic / Responses).
	Name string `json:"name,omitempty"`
	// Type is the JSON "type" field when the payload is a typed event object.
	Type string `json:"type,omitempty"`
	// Data is the raw SSE data payload (typically JSON).
	Data []byte `json:"data,omitempty"`
}

// TokenEstimate is the reservation-time input estimate passed to Begin.
type TokenEstimate struct {
	InputTokens      int `json:"input_tokens,omitempty"`
	TotalReserved    int `json:"total_reserved,omitempty"`
	OutputCapTokens  int `json:"output_cap_tokens,omitempty"`
	ToolSchemaTokens int `json:"tool_schema_tokens,omitempty"`
	ImageTokens      int `json:"image_tokens,omitempty"`
}

// Usage is reconciled token usage passed to Finish.
type Usage struct {
	InputTokens       int  `json:"input_tokens"`
	OutputTokens      int  `json:"output_tokens"`
	TotalTokens       int  `json:"total_tokens"`
	ReasoningTokens   *int `json:"reasoning_tokens,omitempty"`
	CachedInputTokens *int `json:"cached_input_tokens,omitempty"`
}

// StopReason classifies why Finish was invoked and/or the dialect terminal
// stop value when known.
type StopReason string

// ErrTerminal commits a translator to Finish-only after Next. Wrapping or
// returning this error from Next stops further Next calls.
var ErrTerminal = errors.New("stream: terminal")

const (
	StopComplete StopReason = "complete"
	StopCancel   StopReason = "cancel"
	StopError    StopReason = "error"
)
