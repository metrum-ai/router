// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Opaque reasoning provenance wiring (issue #622).
//
// Inbound: opaque reasoning history is unwrapped for the target that minted it
// and dropped, with a bounded trace class, for every other target. Outbound:
// opaque values are wrapped with the served target's binding before the caller
// sees them, in unary and streamed responses.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Provenance modes.
const (
	reasoningProvenanceOff     = "off"
	reasoningProvenanceObserve = "observe"
	reasoningProvenanceEnforce = "enforce"
)

// reasoningProvenanceBinding is the loaded runtime configuration. A zero mode
// means the feature is off and no code path changes.
type reasoningProvenanceBinding struct {
	mode string
	key  provenanceKey
	ttl  time.Duration
}

func (b reasoningProvenanceBinding) off() bool {
	return b.mode == "" || b.mode == reasoningProvenanceOff
}

func (b reasoningProvenanceBinding) wraps() bool {
	return !b.off() && len(b.key.secret) > 0
}

func (b reasoningProvenanceBinding) enforces() bool {
	return b.mode == reasoningProvenanceEnforce
}

// newReasoningProvenanceBinding resolves mode, key and ttl from configuration.
// observe and enforce both require a key; a missing or invalid key is an error
// so a deployment cannot believe it is enforcing when it is not.
func newReasoningProvenanceBinding(cfg ReasoningProvenanceConfig, lookupEnv func(string) string) (reasoningProvenanceBinding, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch mode {
	case "", reasoningProvenanceOff:
		return reasoningProvenanceBinding{mode: reasoningProvenanceOff}, nil
	case reasoningProvenanceObserve, reasoningProvenanceEnforce:
	default:
		return reasoningProvenanceBinding{}, errReasoningProvenanceMode
	}
	envName := strings.TrimSpace(cfg.KeyEnv)
	if envName == "" {
		return reasoningProvenanceBinding{}, errReasoningProvenanceKeyMissing
	}
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	secret := lookupEnv(envName)
	if strings.TrimSpace(secret) == "" {
		return reasoningProvenanceBinding{}, errReasoningProvenanceKeyMissing
	}
	keyID := strings.TrimSpace(cfg.KeyID)
	if keyID == "" {
		keyID = "k1"
	}
	key, err := newReasoningProvenanceKey(keyID, secret)
	if err != nil {
		return reasoningProvenanceBinding{}, err
	}
	ttl := 24 * time.Hour
	if strings.TrimSpace(cfg.TTL) != "" {
		parsed, err := time.ParseDuration(strings.TrimSpace(cfg.TTL))
		if err != nil || parsed <= 0 || parsed > reasoningProvenanceMaxTTL {
			return reasoningProvenanceBinding{}, errReasoningProvenanceTTL
		}
		ttl = parsed
	}
	return reasoningProvenanceBinding{mode: mode, key: key, ttl: ttl}, nil
}

// reasoningProvenanceStats accumulates bounded, value-free diagnostics for one
// request. Only classes and counts are exported.
type reasoningProvenanceStats struct {
	dropped   map[string]int
	wrapped   int
	unwrapped int
}

func (s *reasoningProvenanceStats) drop(class string) {
	if s == nil {
		return
	}
	if s.dropped == nil {
		s.dropped = map[string]int{}
	}
	if len(s.dropped) >= 8 && s.dropped[class] == 0 {
		// Bound distinct classes so a hostile history cannot inflate telemetry.
		class = "other"
	}
	s.dropped[class]++
}

func (s *reasoningProvenanceStats) empty() bool {
	if s == nil {
		return true
	}
	return len(s.dropped) == 0 && s.wrapped == 0 && s.unwrapped == 0
}

// summary renders the bounded trace message without any opaque value.
func (s *reasoningProvenanceStats) summary() string {
	if s == nil {
		return ""
	}
	classes := make([]string, 0, len(s.dropped))
	for class := range s.dropped {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	parts := make([]string, 0, len(classes)+2)
	for _, class := range classes {
		parts = append(parts, class+"="+itoaSmall(s.dropped[class]))
	}
	if s.unwrapped > 0 {
		parts = append(parts, "unwrapped="+itoaSmall(s.unwrapped))
	}
	if s.wrapped > 0 {
		parts = append(parts, "wrapped="+itoaSmall(s.wrapped))
	}
	return strings.Join(parts, " ")
}

func itoaSmall(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// reasoningScopeFor returns the binding scope a target represents. The account
// identity is the operator-declared provider key_id, never the credential.
func reasoningScopeFor(target Target, provider ProviderConfig, dialect, group string) reasoningScope {
	return reasoningScope{
		Dialect:  normalizeDialect(dialect),
		Provider: strings.TrimSpace(target.Provider),
		KeyID:    strings.TrimSpace(provider.KeyID),
		Model:    strings.TrimSpace(target.Model),
		Group:    strings.TrimSpace(group),
	}
}

// opaqueFieldKind reports the provenance kind for an object's opaque field.
func opaqueFieldKind(objectType, field string) (reasoningKind, bool) {
	switch {
	case objectType == "reasoning" && field == "encrypted_content":
		return reasoningKindEncryptedContent, true
	case objectType == "thinking" && field == "signature":
		return reasoningKindThinkingSig, true
	case objectType == "redacted_thinking" && field == "data":
		return reasoningKindRedactedThinking, true
	case objectType == "signature_delta" && field == "signature":
		return reasoningKindThinkingSig, true
	default:
		return "", false
	}
}

// reasoningContainerKeys are the protocol positions where provider-owned
// reasoning objects live. Rewriting is restricted to them so user text, tool
// results, tool arguments and arbitrary metadata are never modified.
var reasoningContainerKeys = map[string]bool{
	"messages":      true,
	"input":         true,
	"output":        true,
	"content":       true,
	"content_block": true,
	"delta":         true,
	"item":          true,
	"message":       true,
}

// rewriteOpaqueReasoningValues walks protocol containers only and applies fn to
// every opaque reasoning field. fn returns a replacement value and whether the
// containing object must be dropped.
func rewriteOpaqueReasoningValues(value any, fn func(kind reasoningKind, value string) (string, bool)) any {
	switch node := value.(type) {
	case []any:
		out := make([]any, 0, len(node))
		for _, item := range node {
			rewritten, drop := rewriteOpaqueReasoningObject(item, fn)
			if drop {
				continue
			}
			out = append(out, rewritten)
		}
		return out
	case map[string]any:
		rewritten, drop := rewriteOpaqueReasoningObject(node, fn)
		if drop {
			return map[string]any{}
		}
		return rewritten
	default:
		return value
	}
}

// rewriteOpaqueReasoningObject rewrites one protocol object: its own opaque
// fields plus any whitelisted nested containers.
func rewriteOpaqueReasoningObject(value any, fn func(kind reasoningKind, value string) (string, bool)) (any, bool) {
	node, ok := value.(map[string]any)
	if !ok {
		// Arrays nested in containers are walked as containers of objects.
		if list, isList := value.([]any); isList {
			return rewriteOpaqueReasoningValues(list, fn), false
		}
		return value, false
	}
	objectType := stringValue(node["type"])
	for field, raw := range node {
		kind, isOpaque := opaqueFieldKind(objectType, field)
		if !isOpaque {
			continue
		}
		text, isString := raw.(string)
		if !isString || text == "" {
			continue
		}
		replacement, dropObject := fn(kind, text)
		if dropObject {
			return nil, true
		}
		node[field] = replacement
	}
	for field, child := range node {
		if !reasoningContainerKeys[field] {
			continue
		}
		switch nested := child.(type) {
		case []any:
			out := make([]any, 0, len(nested))
			for _, item := range nested {
				rewritten, drop := rewriteOpaqueReasoningObject(item, fn)
				if drop {
					continue
				}
				out = append(out, rewritten)
			}
			node[field] = out
		case map[string]any:
			rewritten, drop := rewriteOpaqueReasoningObject(nested, fn)
			if drop {
				delete(node, field)
				continue
			}
			node[field] = rewritten
		}
	}
	return node, false
}

// applyInboundProvenance unwraps history minted for this target and drops it
// for every other target. It returns the request to encode plus stats.
func (b reasoningProvenanceBinding) applyInbound(raw map[string]any, dialect string, scope reasoningScope, now time.Time) (map[string]any, reasoningProvenanceStats) {
	var stats reasoningProvenanceStats
	if !b.wraps() || raw == nil {
		return raw, stats
	}
	rewritten := rewriteOpaqueReasoningValues(raw, func(kind reasoningKind, value string) (string, bool) {
		if !strings.HasPrefix(value, reasoningEnvelopePrefix+".") {
			stats.drop("unknown-origin")
			return value, b.enforces()
		}
		original, err := parseReasoningEnvelope(b.key, value, scope, kind, now)
		if err != nil {
			stats.drop(reasoningProvenanceReason(err))
			return value, b.enforces()
		}
		stats.unwrapped++
		return original, false
	})
	result, ok := rewritten.(map[string]any)
	if !ok {
		return raw, stats
	}
	return result, stats
}

// wrapOutboundValue binds a provider value to the served scope. On any failure
// the original value is returned unchanged so a caller is never handed a
// half-built envelope.
func (b reasoningProvenanceBinding) wrapOutboundValue(kind reasoningKind, value string, scope reasoningScope, now time.Time, stats *reasoningProvenanceStats) string {
	if !b.wraps() || value == "" {
		return value
	}
	if strings.HasPrefix(value, reasoningEnvelopePrefix+".") {
		// Already bound (for example a native round trip through a stored item).
		return value
	}
	if kind == reasoningKindThinkingSig && len(value) > maxReasoningOpaqueBytes {
		return value
	}
	envelope, err := b.key.wrap(scope, kind, value, now, b.ttl)
	if err != nil {
		if stats != nil {
			stats.drop("wrap-failed")
		}
		return value
	}
	if stats != nil {
		stats.wrapped++
	}
	return envelope
}

// wrapOutboundJSON binds opaque values inside a response document.
func (b reasoningProvenanceBinding) wrapOutboundJSON(document map[string]any, scope reasoningScope, now time.Time, stats *reasoningProvenanceStats) map[string]any {
	if !b.wraps() || document == nil {
		return document
	}
	rewritten := rewriteOpaqueReasoningValues(document, func(kind reasoningKind, value string) (string, bool) {
		return b.wrapOutboundValue(kind, value, scope, now, stats), false
	})
	if result, ok := rewritten.(map[string]any); ok {
		return result
	}
	return document
}

// wrapOutboundFrame binds opaque values inside one streamed SSE payload.
func (b reasoningProvenanceBinding) wrapOutboundFrame(data []byte, scope reasoningScope, now time.Time, stats *reasoningProvenanceStats) []byte {
	if !b.wraps() || len(data) == 0 || !hasOpaqueReasoningField(data) {
		return data
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return data
	}
	var local reasoningProvenanceStats
	wrapped := b.wrapOutboundJSON(document, scope, now, &local)
	if wrapped == nil || local.wrapped == 0 {
		// Nothing to bind: return the provider frame byte for byte.
		return data
	}
	mergeProvenanceStats(stats, local)
	encoded, err := json.Marshal(wrapped)
	if err != nil {
		return data
	}
	return encoded
}

// hasOpaqueReasoningField cheaply filters frames before JSON decoding.
func hasOpaqueReasoningField(data []byte) bool {
	return strings.Contains(string(data), `"encrypted_content"`) ||
		strings.Contains(string(data), `"signature"`) ||
		strings.Contains(string(data), `"redacted_thinking"`)
}

// Errors for reasoning provenance configuration.
var (
	errReasoningProvenanceMode       = errors.New("server reasoning_provenance mode must be off, observe or enforce")
	errReasoningProvenanceKeyMissing = errors.New("server reasoning_provenance observe/enforce requires key_env naming a non-empty secret")
	errReasoningProvenanceTTL        = errors.New("server reasoning_provenance ttl must be a positive duration of at most 30d")
)

// validateReasoningProvenanceConfig rejects an enabled mode without a usable
// key reference. The key value itself is read from the environment at startup
// and is never stored in configuration or diagnostics.
func validateReasoningProvenanceConfig(cfg ReasoningProvenanceConfig) error {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch mode {
	case "", reasoningProvenanceOff:
		if strings.TrimSpace(cfg.KeyEnv) != "" || strings.TrimSpace(cfg.KeyID) != "" || strings.TrimSpace(cfg.TTL) != "" {
			return fmt.Errorf("server reasoning_provenance key_env, key_id and ttl require mode observe or enforce")
		}
		return nil
	case reasoningProvenanceObserve, reasoningProvenanceEnforce:
	default:
		return errReasoningProvenanceMode
	}
	if strings.TrimSpace(cfg.KeyEnv) == "" {
		return errReasoningProvenanceKeyMissing
	}
	if !validEnvName(strings.TrimSpace(cfg.KeyEnv)) {
		return fmt.Errorf("server reasoning_provenance key_env must be a valid environment variable name")
	}
	if keyID := strings.TrimSpace(cfg.KeyID); keyID != "" && !reasoningEnvelopeKeyIDPattern.MatchString(keyID) {
		return fmt.Errorf("server reasoning_provenance key_id must match %s", reasoningEnvelopeKeyIDPattern)
	}
	if strings.TrimSpace(cfg.TTL) != "" {
		ttl, err := time.ParseDuration(strings.TrimSpace(cfg.TTL))
		if err != nil || ttl <= 0 || ttl > reasoningProvenanceMaxTTL {
			return errReasoningProvenanceTTL
		}
	}
	return nil
}

// mergeProvenanceStats accumulates one attempt's counters into the request.
func mergeProvenanceStats(dst *reasoningProvenanceStats, src reasoningProvenanceStats) {
	if dst == nil {
		return
	}
	for class, count := range src.dropped {
		for i := 0; i < count; i++ {
			dst.drop(class)
		}
	}
	dst.wrapped += src.wrapped
	dst.unwrapped += src.unwrapped
}

// wrapOutboundRaw binds opaque values inside a unary response document. It
// returns the original bytes when nothing changed or parsing failed, so a
// provider response is never corrupted by provenance handling.
func (b reasoningProvenanceBinding) wrapOutboundRaw(raw []byte, scope reasoningScope, now time.Time, stats *reasoningProvenanceStats) []byte {
	if !b.wraps() || len(raw) == 0 || !hasOpaqueReasoningField(raw) {
		return raw
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return raw
	}
	wrapped := rewriteOpaqueReasoningValues(document, func(kind reasoningKind, value string) (string, bool) {
		return b.wrapOutboundValue(kind, value, scope, now, stats), false
	})
	result, ok := wrapped.(map[string]any)
	if !ok {
		return raw
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return raw
	}
	return encoded
}
