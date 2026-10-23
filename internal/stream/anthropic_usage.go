// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

// AnthropicUsageMap partitions inclusive router input into Anthropic's three
// disjoint categories. Never emit cached tokens twice as input_tokens.
func AnthropicUsageMap(u Usage) map[string]any {
	in := u.InputTokens
	out := map[string]any{"output_tokens": u.OutputTokens}
	if u.CachedInputTokens != nil {
		in -= *u.CachedInputTokens
		out["cache_read_input_tokens"] = *u.CachedInputTokens
	}
	if u.CacheWriteInputTokens != nil {
		in -= *u.CacheWriteInputTokens
		out["cache_creation_input_tokens"] = *u.CacheWriteInputTokens
	}
	if in < 0 {
		in = 0
	}
	out["input_tokens"] = in
	return out
}

type anthropicUsageFields struct {
	Input    *int `json:"input_tokens"`
	Output   *int `json:"output_tokens"`
	Cached   *int `json:"cache_read_input_tokens"`
	Write    *int `json:"cache_creation_input_tokens"`
	Creation *struct {
		Five *int `json:"ephemeral_5m_input_tokens"`
		Hour *int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

func (f anthropicUsageFields) apply(u *Usage, uncached *int) {
	if f.Input != nil {
		*uncached = *f.Input
	}
	if f.Output != nil {
		u.OutputTokens = *f.Output
	}
	if f.Cached != nil {
		u.CachedInputTokens = f.Cached
	}
	if f.Write != nil {
		u.CacheWriteInputTokens = f.Write
	} else if f.Creation != nil {
		n := 0
		if f.Creation.Five != nil {
			n += *f.Creation.Five
		}
		if f.Creation.Hour != nil {
			n += *f.Creation.Hour
		}
		u.CacheWriteInputTokens = &n
	}
	u.InputTokens = *uncached
	if u.CachedInputTokens != nil {
		u.InputTokens += *u.CachedInputTokens
	}
	if u.CacheWriteInputTokens != nil {
		u.InputTokens += *u.CacheWriteInputTokens
	}
	u.TotalTokens = u.InputTokens + u.OutputTokens
}
