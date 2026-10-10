// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

type responseCache struct {
	mu       sync.Mutex
	enabled  bool
	maxBytes int64
	ttl      time.Duration
	bytes    int64
	ll       *list.List
	items    map[string]*list.Element
}

type cacheStats struct {
	Enabled      bool
	Items        int64
	Bytes        int64
	MaxBytes     int64
	OccupancyPct float64
}

type cacheEntry struct {
	key       string
	resp      *IRResponse
	size      int64
	expiresAt time.Time
}

func newCache(cfg CacheConfig) *responseCache {
	return &responseCache{
		enabled:  cfg.Enabled,
		maxBytes: cfg.MaxBytes,
		ttl:      cfg.DefaultTTL,
		ll:       list.New(),
		items:    map[string]*list.Element{},
	}
}

func cacheKey(req *IRRequest, target Target, callerID, project string) string {
	type normalized struct {
		CallerID           string          `json:"caller_id,omitempty"`
		Project            string          `json:"project,omitempty"`
		Model              string          `json:"model"`
		System             string          `json:"system,omitempty"`
		Messages           []IRMessage     `json:"messages,omitempty"`
		Input              string          `json:"input,omitempty"`
		MaxTokens          int             `json:"max_tokens,omitempty"`
		MaxTokensField     string          `json:"max_tokens_field,omitempty"`
		Temperature        *float64        `json:"temperature,omitempty"`
		Stop               []string        `json:"stop,omitempty"`
		Thinking           map[string]any  `json:"thinking,omitempty"`
		Reasoning          ReasoningIntent `json:"reasoning,omitempty"`
		TopP               any             `json:"top_p,omitempty"`
		Seed               any             `json:"seed,omitempty"`
		FrequencyPenalty   any             `json:"frequency_penalty,omitempty"`
		PresencePenalty    any             `json:"presence_penalty,omitempty"`
		LogitBias          any             `json:"logit_bias,omitempty"`
		PreviousResponseID string          `json:"previous_response_id,omitempty"`
		ReasoningEffort    any             `json:"reasoning_effort,omitempty"`
		N                  any             `json:"n,omitempty"`
		Provider           string          `json:"provider"`
		TargetModel        string          `json:"target_model"`
	}
	n := normalized{
		CallerID:       callerID,
		Project:        project,
		Model:          req.Model,
		System:         req.System,
		Messages:       req.Messages,
		Input:          req.Input,
		MaxTokens:      req.MaxTokens,
		MaxTokensField: req.MaxTokensField,
		Temperature:    req.Temperature,
		Stop:           req.Stop,
		Thinking:       req.Thinking,
		Reasoning:      req.Reasoning,
		Provider:       target.Provider,
		TargetModel:    target.Model,
	}
	if req.Raw != nil {
		n.TopP = req.Raw["top_p"]
		n.Seed = req.Raw["seed"]
		n.FrequencyPenalty = req.Raw["frequency_penalty"]
		n.PresencePenalty = req.Raw["presence_penalty"]
		n.LogitBias = req.Raw["logit_bias"]
		n.PreviousResponseID = strings.TrimSpace(stringValue(req.Raw["previous_response_id"]))
		n.ReasoningEffort = req.Raw["reasoning_effort"]
		// n changes how many choices come back, so it is part of response identity.
		n.N = req.Raw["n"]
		if n.Thinking == nil {
			if thinking, ok := req.Raw["thinking"].(map[string]any); ok {
				n.Thinking = thinking
			}
		}
	}
	raw, _ := json.Marshal(n)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// cacheRawFieldAllowlist enumerates Raw keys that are either represented in the
// cache key or known to be safe to ignore for deterministic unary caching.
var cacheRawFieldAllowlist = map[string]bool{
	"model":                 true,
	"messages":              true,
	"input":                 true,
	"system":                true,
	"max_tokens":            true,
	"max_completion_tokens": true,
	"max_output_tokens":     true,
	"temperature":           true,
	"stop":                  true,
	"stop_sequences":        true,
	"stream":                true,
	"tools":                 true,
	"tool_choice":           true,
	"response_format":       true,
	"thinking":              true,
	"reasoning":             true,
	"reasoning_effort":      true,
	"top_p":                 true,
	"seed":                  true,
	"frequency_penalty":     true,
	"presence_penalty":      true,
	"logit_bias":            true,
	"previous_response_id":  true,
	"metadata":              true,
	"user":                  true,
	"n":                     true,
	"stream_options":        true,
	// Caller/request identifiers must not affect cache identity.
	"id": true,
}

func cacheable(req *IRRequest) bool {
	if req.Stream || req.NoCache || len(req.Tools) > 0 || requestHasImages(req) || requestHasStructuredOutput(req) {
		return false
	}
	// Omitted temperature must not be cached: providers may sample nondeterministically.
	if req.Temperature == nil || *req.Temperature != 0 {
		return false
	}
	// Bypass when the request carries behavior-changing Raw fields outside the
	// supported cache contract. Unknown fields must not share a cached identity.
	if req.Raw != nil {
		for key := range req.Raw {
			if !cacheRawFieldAllowlist[key] {
				return false
			}
		}
	}
	return true
}

func (c *responseCache) Get(key string) (*IRResponse, bool) {
	if c == nil || !c.enabled {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	ent := el.Value.(*cacheEntry)
	if time.Now().After(ent.expiresAt) {
		c.remove(el)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return cloneCachedResponse(ent.resp), true
}

func (c *responseCache) Put(key string, resp *IRResponse) {
	if c == nil || !c.enabled || resp == nil {
		return
	}
	cached := sanitizeCachedResponse(resp)
	raw, _ := json.Marshal(cached)
	size := int64(len(raw))
	if size > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.remove(el)
	}
	ent := &cacheEntry{key: key, resp: cached, size: size, expiresAt: time.Now().Add(c.ttl)}
	el := c.ll.PushFront(ent)
	c.items[key] = el
	c.bytes += size
	for c.bytes > c.maxBytes && c.ll.Len() > 0 {
		c.remove(c.ll.Back())
	}
}

func (c *responseCache) Stats() cacheStats {
	if c == nil {
		return cacheStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.removeExpiredLocked(now)
	stats := cacheStats{
		Enabled:  c.enabled,
		Items:    int64(len(c.items)),
		Bytes:    c.bytes,
		MaxBytes: c.maxBytes,
	}
	if stats.MaxBytes > 0 {
		stats.OccupancyPct = float64(stats.Bytes) * 100 / float64(stats.MaxBytes)
	}
	return stats
}

func (c *responseCache) removeExpiredLocked(now time.Time) {
	for el := c.ll.Back(); el != nil; {
		prev := el.Prev()
		ent := el.Value.(*cacheEntry)
		if now.After(ent.expiresAt) {
			c.remove(el)
		}
		el = prev
	}
}

func sanitizeCachedResponse(resp *IRResponse) *IRResponse {
	if resp == nil {
		return nil
	}
	out := &IRResponse{
		Model:      resp.Model,
		Text:       resp.Text,
		StopReason: resp.StopReason,
		Usage:      resp.Usage,
	}
	if len(resp.Warnings) > 0 {
		out.Warnings = append([]string(nil), resp.Warnings...)
	}
	return out
}

func cloneCachedResponse(resp *IRResponse) *IRResponse {
	if resp == nil {
		return nil
	}
	out := *resp
	if len(resp.Warnings) > 0 {
		out.Warnings = append([]string(nil), resp.Warnings...)
	}
	out.ID = ""
	out.Raw = nil
	out.Headers = nil
	return &out
}

func (c *responseCache) remove(el *list.Element) {
	if el == nil {
		return
	}
	ent := el.Value.(*cacheEntry)
	delete(c.items, ent.key)
	c.bytes -= ent.size
	c.ll.Remove(el)
}
