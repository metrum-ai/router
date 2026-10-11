// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultBridgeStatefulSessionHeader = "X-Router-Session"
const defaultBridgeStatefulSessionBackend = "memory"
const defaultBridgeStatefulSessionRedisNamespace = "metrum-ai-router"

type bridgeSessionBackend interface {
	Get(ctx context.Context, key string) (bridgeSessionEntry, bool, error)
	Set(ctx context.Context, key, previousResponseID string, ttl time.Duration, maxEntries int) error
	Delete(ctx context.Context, key string) error
	Close() error
}

type bridgeSessionBackends struct {
	memory *bridgeSessionStore
	redis  map[bridgeRedisBackendFingerprint]bridgeSessionBackend
}

type bridgeSessionStore struct {
	mu      sync.Mutex
	entries map[string]bridgeSessionEntry
	now     func() time.Time
}

type bridgeSessionEntry struct {
	PreviousResponseID string
	ExpiresAt          time.Time
	UpdatedAt          time.Time
}

type bridgeSessionLookup struct {
	Enabled            bool
	Requested          bool
	Header             string
	Key                string
	PreviousResponseID string
	TTL                time.Duration
	MaxEntries         int
}

func newBridgeSessionStore() *bridgeSessionStore {
	return &bridgeSessionStore{
		entries: map[string]bridgeSessionEntry{},
		now:     time.Now,
	}
}

func newBridgeSessionBackends(ctx context.Context, cfg *Config) (*bridgeSessionBackends, error) {
	backends := &bridgeSessionBackends{
		memory: newBridgeSessionStore(),
		redis:  map[bridgeRedisBackendFingerprint]bridgeSessionBackend{},
	}
	if cfg == nil {
		return backends, nil
	}
	for groupName, group := range cfg.Models {
		for _, target := range group.Targets {
			resolved, err := cfg.resolveTarget(groupName, target)
			if err != nil {
				return nil, err
			}
			sessionCfg := bridgeStatefulSessionConfig(resolved)
			if !sessionCfg.Enabled || bridgeStatefulSessionBackendName(sessionCfg) != "redis" {
				continue
			}
			key := bridgeRedisBackendKey(sessionCfg.Redis)
			if existing, ok := backends.redis[key]; ok {
				if store, ok := existing.(*redisBridgeSessionStore); ok {
					store.extendLegacyKeyWindow(bridgeStatefulSessionTTL(sessionCfg))
				}
				continue
			}
			store, err := newRedisBridgeSessionStore(ctx, sessionCfg.Redis)
			if err != nil {
				backends.Close()
				return nil, fmt.Errorf("chat_to_responses stateful_sessions redis backend for model group %s: %w", groupName, err)
			}
			store.extendLegacyKeyWindow(bridgeStatefulSessionTTL(sessionCfg))
			backends.redis[key] = store
		}
	}
	return backends, nil
}

func (b *bridgeSessionBackends) backend(cfg BridgeStatefulSessionsConfig) (bridgeSessionBackend, error) {
	if b == nil {
		return nil, fmt.Errorf("bridge session backend registry is nil")
	}
	switch bridgeStatefulSessionBackendName(cfg) {
	case "memory":
		if b.memory == nil {
			return nil, fmt.Errorf("memory bridge session backend is nil")
		}
		return b.memory, nil
	case "redis":
		key := bridgeRedisBackendKey(cfg.Redis)
		backend, ok := b.redis[key]
		if !ok || backend == nil {
			return nil, fmt.Errorf("redis bridge session backend is not initialized")
		}
		return backend, nil
	default:
		return nil, fmt.Errorf("unsupported bridge session backend")
	}
}

func (b *bridgeSessionBackends) Close() {
	if b == nil {
		return
	}
	for _, backend := range b.redis {
		if backend == nil {
			continue
		}
		_ = backend.Close()
	}
}

func (s *bridgeSessionStore) Get(ctx context.Context, key string) (bridgeSessionEntry, bool, error) {
	if s == nil || strings.TrimSpace(key) == "" {
		return bridgeSessionEntry{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return bridgeSessionEntry{}, false, nil
	}
	now := s.now()
	if !entry.ExpiresAt.IsZero() && !entry.ExpiresAt.After(now) {
		delete(s.entries, key)
		return bridgeSessionEntry{}, false, nil
	}
	return entry, true, nil
}

func (s *bridgeSessionStore) Set(ctx context.Context, key, previousResponseID string, ttl time.Duration, maxEntries int) error {
	if s == nil || strings.TrimSpace(key) == "" || strings.TrimSpace(previousResponseID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	entry := bridgeSessionEntry{PreviousResponseID: previousResponseID, UpdatedAt: now}
	if ttl > 0 {
		entry.ExpiresAt = now.Add(ttl)
	}
	s.entries[key] = entry
	s.pruneLocked(now, maxEntries)
	return nil
}

func (s *bridgeSessionStore) Delete(ctx context.Context, key string) error {
	if s == nil || strings.TrimSpace(key) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
	return nil
}

func (s *bridgeSessionStore) Close() error {
	return nil
}

func (s *bridgeSessionStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now, 0)
	return len(s.entries)
}

func (s *bridgeSessionStore) pruneLocked(now time.Time, maxEntries int) {
	for key, entry := range s.entries {
		if !entry.ExpiresAt.IsZero() && !entry.ExpiresAt.After(now) {
			delete(s.entries, key)
		}
	}
	if maxEntries <= 0 || len(s.entries) <= maxEntries {
		return
	}
	type candidate struct {
		key       string
		updatedAt time.Time
	}
	candidates := make([]candidate, 0, len(s.entries))
	for key, entry := range s.entries {
		candidates = append(candidates, candidate{key: key, updatedAt: entry.UpdatedAt})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].updatedAt.Before(candidates[j].updatedAt)
	})
	for len(s.entries) > maxEntries && len(candidates) > 0 {
		delete(s.entries, candidates[0].key)
		candidates = candidates[1:]
	}
}

func (s *Service) chatToResponsesBridgeSession(ctx context.Context, rc *requestContext, groupName string, target Target, attemptIndex int) bridgeSessionLookup {
	cfg := bridgeStatefulSessionConfig(target)
	if rc == nil || !cfg.Enabled || !isChatToResponsesBridge(rc.dialect, targetDialect(s.cfg.Provider[target.Provider], target), target) {
		return bridgeSessionLookup{}
	}
	header := bridgeStatefulSessionHeader(cfg)
	sessionID := ""
	if rc != nil && rc.bridgeSessionHeaders != nil {
		sessionID = rc.bridgeSessionHeaders[strings.ToLower(header)]
	}
	if strings.TrimSpace(sessionID) == "" {
		return bridgeSessionLookup{Enabled: true, Header: header}
	}
	key := bridgeSessionKey(rc, groupName, target, sessionID)
	lookup := bridgeSessionLookup{
		Enabled:    true,
		Requested:  true,
		Header:     header,
		Key:        key,
		TTL:        bridgeStatefulSessionTTL(cfg),
		MaxEntries: bridgeStatefulSessionMaxEntries(cfg),
	}
	backend, err := s.bridgeSessions.backend(cfg)
	if err != nil {
		rc.trace("bridge_session_backend_error", "chat-to-responses stateful session backend unavailable", target, attemptIndex, 0, "bridge_session_backend", false, 0)
		return lookup
	}
	start := time.Now()
	entry, ok, err := backend.Get(ctx, key)
	if err != nil {
		rc.trace("bridge_session_backend_error", "chat-to-responses stateful session get failed", target, attemptIndex, 0, "bridge_session_get", false, durationMillis(time.Since(start)))
		return lookup
	}
	if ok {
		lookup.PreviousResponseID = entry.PreviousResponseID
		rc.trace("bridge_session_lookup_hit", "chat-to-responses stateful session previous_response_id found", target, attemptIndex, 0, "", false, durationMillis(time.Since(start)))
	} else {
		rc.trace("bridge_session_lookup_miss", "chat-to-responses stateful session previous_response_id not found", target, attemptIndex, 0, "", false, durationMillis(time.Since(start)))
	}
	return lookup
}

func (s *Service) setChatToResponsesBridgeSession(ctx context.Context, rc *requestContext, lookup bridgeSessionLookup, target Target, previousResponseID string, attemptIndex int) {
	if !lookup.Requested || strings.TrimSpace(previousResponseID) == "" {
		return
	}
	cfg := bridgeStatefulSessionConfig(target)
	backend, err := s.bridgeSessions.backend(cfg)
	if err != nil {
		rc.trace("bridge_session_backend_error", "chat-to-responses stateful session backend unavailable", target, attemptIndex, 0, "bridge_session_backend", false, 0)
		return
	}
	start := time.Now()
	if err := backend.Set(ctx, lookup.Key, previousResponseID, lookup.TTL, lookup.MaxEntries); err != nil {
		rc.trace("bridge_session_backend_error", "chat-to-responses stateful session set failed", target, attemptIndex, 0, "bridge_session_set", false, durationMillis(time.Since(start)))
		return
	}
	rc.trace("bridge_session_set", "chat-to-responses stateful session previous_response_id stored", target, attemptIndex, 0, "", false, durationMillis(time.Since(start)))
}

func (s *Service) deleteChatToResponsesBridgeSession(ctx context.Context, rc *requestContext, lookup bridgeSessionLookup, target Target, attemptIndex int) error {
	cfg := bridgeStatefulSessionConfig(target)
	backend, err := s.bridgeSessions.backend(cfg)
	if err != nil {
		rc.trace("bridge_session_backend_error", "chat-to-responses stateful session backend unavailable", target, attemptIndex, 0, "bridge_session_backend", false, 0)
		return err
	}
	start := time.Now()
	if err := backend.Delete(ctx, lookup.Key); err != nil {
		rc.trace("bridge_session_backend_error", "chat-to-responses stateful session delete failed", target, attemptIndex, 0, "bridge_session_delete", false, durationMillis(time.Since(start)))
		return err
	}
	rc.trace("bridge_session_delete", "chat-to-responses stateful session previous_response_id deleted", target, attemptIndex, 0, "", false, durationMillis(time.Since(start)))
	return nil
}

func bridgeStatefulSessionConfig(target Target) BridgeStatefulSessionsConfig {
	cfg := target.Bridges.ChatToResponses.StatefulSessions
	if strings.TrimSpace(cfg.Backend) == "" {
		cfg.Backend = defaultBridgeStatefulSessionBackend
	}
	if strings.TrimSpace(cfg.SessionHeader) == "" {
		cfg.SessionHeader = defaultBridgeStatefulSessionHeader
	}
	if cfg.TTLSeconds == 0 {
		cfg.TTLSeconds = 3600
	}
	if cfg.MaxEntries == 0 {
		cfg.MaxEntries = 10000
	}
	cfg.Redis = bridgeStatefulSessionRedisConfig(cfg.Redis)
	return cfg
}

func bridgeStatefulSessionBackendName(cfg BridgeStatefulSessionsConfig) string {
	backend := strings.ToLower(strings.TrimSpace(cfg.Backend))
	if backend == "" {
		return defaultBridgeStatefulSessionBackend
	}
	return backend
}

func bridgeStatefulSessionRedisConfig(cfg BridgeStatefulSessionsRedisConfig) BridgeStatefulSessionsRedisConfig {
	if strings.TrimSpace(cfg.Namespace) == "" {
		cfg.Namespace = defaultBridgeStatefulSessionRedisNamespace
	}
	if cfg.ConnectTimeoutMS == 0 {
		cfg.ConnectTimeoutMS = 500
	}
	if cfg.ReadTimeoutMS == 0 {
		cfg.ReadTimeoutMS = 500
	}
	if cfg.WriteTimeoutMS == 0 {
		cfg.WriteTimeoutMS = 500
	}
	return cfg
}

func bridgeStatefulSessionHeader(cfg BridgeStatefulSessionsConfig) string {
	header := strings.TrimSpace(cfg.SessionHeader)
	if header == "" {
		header = defaultBridgeStatefulSessionHeader
	}
	return http.CanonicalHeaderKey(header)
}

func bridgeStatefulSessionTTL(cfg BridgeStatefulSessionsConfig) time.Duration {
	if cfg.TTLSeconds <= 0 {
		return time.Hour
	}
	return time.Duration(cfg.TTLSeconds) * time.Second
}

func bridgeStatefulSessionMaxEntries(cfg BridgeStatefulSessionsConfig) int {
	if cfg.MaxEntries <= 0 {
		return 10000
	}
	return cfg.MaxEntries
}

func collectBridgeSessionHeaders(headers http.Header, targets []Target) map[string]string {
	values := map[string]string{}
	for _, target := range targets {
		cfg := target.Bridges.ChatToResponses.StatefulSessions
		if !cfg.Enabled {
			continue
		}
		header := bridgeStatefulSessionHeader(cfg)
		if value := strings.TrimSpace(headers.Get(header)); value != "" {
			values[strings.ToLower(header)] = value
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func bridgeSessionKey(rc *requestContext, groupName string, target Target, sessionID string) string {
	parts := []string{
		"chat-to-responses",
		groupName,
		target.Provider,
		target.Model,
		target.Dialect,
		strings.TrimSpace(sessionID),
	}
	if rc != nil {
		if rc.caller != nil {
			parts = append(parts,
				rc.caller.cfg.ID,
				rc.caller.ownerUser,
				rc.caller.project,
				rc.caller.cfg.Environment,
			)
		}
		parts = append(parts, publicTokenID(rc.rec.TokenID))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func chatResponsesBridgeStaleStateRetryAllowed(bridge bool, lookup bridgeSessionLookup, status int, raw []byte, alreadyRetried bool) bool {
	if !bridge || alreadyRetried || !lookup.Requested || strings.TrimSpace(lookup.PreviousResponseID) == "" || strings.TrimSpace(lookup.Key) == "" {
		return false
	}
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
	default:
		return false
	}
	text := strings.ToLower(string(raw))
	if text == "" {
		return false
	}
	for _, needle := range []string{
		"previous_response_id",
		"previous response",
		"previous responses",
		"prior response",
		"response id is stale",
		"response id has expired",
		"response id not found",
		"conversation expired",
		"conversation not found",
		"conversation state",
		"conversation is stale",
		"stale conversation",
		"expired conversation",
		"stale state",
	} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func validHTTPHeaderName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, r := range name {
		if r > 127 {
			return false
		}
		if !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", r) {
			return false
		}
	}
	return true
}
