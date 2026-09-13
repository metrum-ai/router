// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "encoding/json"

type IRRequest struct {
	Model          string            `json:"model"`
	System         string            `json:"system,omitempty"`
	Messages       []IRMessage       `json:"messages,omitempty"`
	Input          string            `json:"input,omitempty"`
	InputParts     []IRContentPart   `json:"input_parts,omitempty"`
	Tools          []map[string]any  `json:"tools,omitempty"`
	MaxTokens      int               `json:"max_tokens,omitempty"`
	MaxTokensField string            `json:"-"`
	Temperature    *float64          `json:"temperature,omitempty"`
	Stream         bool              `json:"stream,omitempty"`
	Thinking       map[string]any    `json:"thinking,omitempty"`
	Reasoning      ReasoningIntent   `json:"reasoning,omitempty"`
	Stop           []string          `json:"stop,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Raw            map[string]any    `json:"raw,omitempty"`
	NoCache        bool              `json:"no_cache,omitempty"`
}

type ReasoningIntent struct {
	Requested    bool   `json:"requested,omitempty"`
	Source       string `json:"source,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Effort       string `json:"effort,omitempty"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Summary      string `json:"summary,omitempty"`
	Disabled     bool   `json:"disabled,omitempty"`
}

type IRMessage struct {
	Role    string          `json:"role"`
	Content string          `json:"content"`
	Parts   []IRContentPart `json:"parts,omitempty"`
}

type IRContentPart struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ImageURL  string `json:"image_url,omitempty"`
	Detail    string `json:"detail,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	FileID    string `json:"file_id,omitempty"`
}

type IRResponse struct {
	ID          string            `json:"id"`
	Model       string            `json:"model"`
	Text        string            `json:"text"`
	StopReason  string            `json:"stop_reason"`
	Usage       Usage             `json:"usage"`
	Raw         map[string]any    `json:"raw,omitempty"`
	RawResponse bool              `json:"raw_response,omitempty"`
	Streamed    bool              `json:"-"`
	Warnings    []string          `json:"warnings,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	// ReasoningTokens is nil when the upstream omitted the metric; reported zero remains distinct.
	ReasoningTokens *int `json:"reasoning_tokens,omitempty"`
	// CachedInputTokens is nil when the upstream omitted cache-read evidence; reported zero remains distinct.
	CachedInputTokens             *int    `json:"cached_input_tokens,omitempty"`
	InputImageTokens              int     `json:"input_image_tokens,omitempty"`
	UpstreamReportedInputCostUSD  float64 `json:"upstream_reported_input_cost_usd,omitempty"`
	UpstreamReportedOutputCostUSD float64 `json:"upstream_reported_output_cost_usd,omitempty"`
	UpstreamReportedTotalCostUSD  float64 `json:"upstream_reported_total_cost_usd,omitempty"`
}

func estimateTokens(r *IRRequest) int {
	if r == nil {
		return 1
	}
	chars := len(r.System) + len(r.Input)
	for _, p := range r.InputParts {
		chars += len(p.Text)
		if p.Type == "image" {
			chars += 1024
		}
	}
	for _, m := range r.Messages {
		chars += len(m.Role) + len(m.Content)
		for _, p := range m.Parts {
			chars += len(p.Text)
			if p.Type == "image" {
				chars += 1024
			}
		}
	}
	chars += schemaPayloadChars(r)
	if chars == 0 {
		return 1
	}
	return chars/4 + 1
}

func schemaPayloadChars(r *IRRequest) int {
	if r == nil {
		return 0
	}
	chars := jsonValueLen(r.Tools)
	if r.Raw != nil {
		for _, key := range []string{"tools", "response_format", "text"} {
			if key == "tools" && len(r.Tools) > 0 {
				continue
			}
			if value, ok := r.Raw[key]; ok {
				chars += jsonValueLen(value)
			}
		}
	}
	return chars
}

func jsonValueLen(v any) int {
	if v == nil {
		return 0
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(raw)
}

func reservationEstimate(r *IRRequest, dialect string) int {
	input := estimateTokens(r)
	output := 0
	if r != nil && r.MaxTokens > 0 {
		output = r.MaxTokens
	} else if dialect == "anthropic" {
		output = 1024
	}
	return input + output
}
