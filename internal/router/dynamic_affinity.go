// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

const (
	defaultDynamicAffinityTTL        = 10 * time.Minute
	defaultDynamicAffinityMaxEntries = 10000
)

type dynamicAffinityStore struct {
	mu      sync.Mutex
	entries map[string]dynamicAffinityEntry
	now     func() time.Time
}

type dynamicAffinityEntry struct {
	Provider  string
	Model     string
	Dialect   string
	Index     int
	ExpiresAt time.Time
	UpdatedAt time.Time
}

func newDynamicAffinityStore() *dynamicAffinityStore {
	return &dynamicAffinityStore{entries: map[string]dynamicAffinityEntry{}, now: time.Now}
}

func (s *dynamicAffinityStore) get(key string) (dynamicAffinityEntry, string) {
	if s == nil || key == "" {
		return dynamicAffinityEntry{}, "miss"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return dynamicAffinityEntry{}, "miss"
	}
	if !entry.ExpiresAt.After(s.now()) {
		delete(s.entries, key)
		return dynamicAffinityEntry{}, "expired"
	}
	return entry, "hit"
}

func (s *dynamicAffinityStore) set(key string, candidate dynamicCandidate, ttl time.Duration, maxEntries int) {
	if s == nil || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	oldestKey := ""
	var oldestUpdatedAt time.Time
	for existingKey, entry := range s.entries {
		if !entry.ExpiresAt.After(now) {
			delete(s.entries, existingKey)
			continue
		}
		if oldestKey == "" || entry.UpdatedAt.Before(oldestUpdatedAt) {
			oldestKey = existingKey
			oldestUpdatedAt = entry.UpdatedAt
		}
	}
	if maxEntries <= 0 {
		maxEntries = defaultDynamicAffinityMaxEntries
	}
	if _, exists := s.entries[key]; !exists && len(s.entries) >= maxEntries {
		if oldestKey != "" {
			delete(s.entries, oldestKey)
		}
	}
	s.entries[key] = dynamicAffinityEntry{
		Provider:  candidate.Target.Provider,
		Model:     candidate.Target.Model,
		Dialect:   candidate.Target.Dialect,
		Index:     candidate.Index,
		UpdatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
}

func dynamicAffinityEnabled(cfg DynamicScoreAffinityConfig) bool {
	return cfg.Enabled == nil || *cfg.Enabled
}

func dynamicAffinityTTL(cfg DynamicScoreAffinityConfig) time.Duration {
	if cfg.TTLSeconds <= 0 {
		return defaultDynamicAffinityTTL
	}
	return time.Duration(cfg.TTLSeconds) * time.Second
}

func dynamicAffinityMaxEntries(cfg DynamicScoreAffinityConfig) int {
	if cfg.MaxEntries <= 0 {
		return defaultDynamicAffinityMaxEntries
	}
	return cfg.MaxEntries
}

func dynamicAffinityKey(caller *callerRuntime, groupName, callerDialect string, req *IRRequest) string {
	if caller == nil || strings.TrimSpace(caller.cfg.ID) == "" || req == nil {
		return ""
	}
	type prefix struct {
		CallerID string          `json:"caller_id"`
		Group    string          `json:"group"`
		Dialect  string          `json:"dialect"`
		Session  string          `json:"session,omitempty"`
		System   string          `json:"system,omitempty"`
		Message  *IRMessage      `json:"message,omitempty"`
		Input    string          `json:"input,omitempty"`
		Parts    []IRContentPart `json:"parts,omitempty"`
	}
	value := prefix{
		CallerID: caller.cfg.ID,
		Group:    groupName,
		Dialect:  normalizeDialect(callerDialect),
		System:   req.System,
	}
	if session := sanitizeConversationID(requestMetadataValue(req, "session_id", "sessionId")); session != "" {
		value.Session = session
		value.System = ""
	} else if len(req.Messages) > 0 {
		first := req.Messages[0]
		value.Message = &first
	} else {
		value.Input = req.Input
		value.Parts = req.InputParts
	}
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Service) applyDynamicAffinity(cfg DynamicScoreConfig, caller *callerRuntime, groupName, callerDialect string, req *IRRequest, ordered []dynamicCandidate) ([]dynamicCandidate, routingSignalLogRecord) {
	if !dynamicAffinityEnabled(cfg.Affinity) {
		return ordered, routingSignalLogRecord{Strategy: "dynamic_score", SignalName: "affinity_disabled", Source: "dynamic_score", BoolValue: true}
	}
	key := dynamicAffinityKey(caller, groupName, callerDialect, req)
	if key == "" || s == nil || s.affinity == nil || len(ordered) == 0 {
		return ordered, routingSignalLogRecord{}
	}
	entry, status := s.affinity.get(key)
	if status == "hit" {
		for i, candidate := range ordered {
			if dynamicAffinityMatches(entry, candidate) {
				pinned := append([]dynamicCandidate(nil), ordered...)
				copy(pinned[1:i+1], pinned[0:i])
				pinned[0] = candidate
				return pinned, routingSignalLogRecord{Strategy: "dynamic_score", SignalName: "affinity_hit", Source: "dynamic_score", CandidateIndex: candidate.Index, BoolValue: true}
			}
		}
		status = "ineligible"
	}
	s.affinity.set(key, ordered[0], dynamicAffinityTTL(cfg.Affinity), dynamicAffinityMaxEntries(cfg.Affinity))
	return ordered, routingSignalLogRecord{Strategy: "dynamic_score", SignalName: "affinity_" + status, Source: "dynamic_score", CandidateIndex: ordered[0].Index, BoolValue: true}
}

func dynamicAffinityMatches(entry dynamicAffinityEntry, candidate dynamicCandidate) bool {
	return entry.Index == candidate.Index &&
		entry.Provider == candidate.Target.Provider &&
		entry.Model == candidate.Target.Model &&
		entry.Dialect == candidate.Target.Dialect
}

func appendDynamicAffinitySignal(signals []routingSignalLogRecord, signal routingSignalLogRecord) []routingSignalLogRecord {
	if signal.SignalName == "" {
		return signals
	}
	signal.Seq = len(signals) + 1
	return append(signals, signal)
}
