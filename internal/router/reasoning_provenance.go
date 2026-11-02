// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Opaque provider reasoning provenance (issue #622).
//
// Provider-owned reasoning continuations (OpenAI Responses `encrypted_content`,
// Anthropic thinking signatures and `redacted_thinking` data) are only meaningful to
// the provider account, model and dialect that minted them. This file defines
// the binding envelope and its parser. It performs no request or response
// wiring: nothing here is called until the observe/enforce phase lands.

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	// reasoningProvenanceVersion is the envelope payload schema version.
	reasoningProvenanceVersion = 1
	// reasoningEnvelopePrefix marks a router-owned opaque reasoning value.
	reasoningEnvelopePrefix = "rtr_rz1"
	// maxReasoningOpaqueBytes bounds the wrapped provider value itself. Provider
	// values are not small: OpenAI Responses encrypted_content grows with the
	// reasoning turn, and Anthropic thinking signatures and redacted_thinking
	// data are opaque blobs. 256 KiB leaves room for both without letting a
	// single history item dominate request parsing.
	maxReasoningOpaqueBytes = 256 << 10
	// maxReasoningPayloadBytes bounds the decoded payload section: base64 of one
	// maximum opaque value plus JSON overhead, rounded up.
	maxReasoningPayloadBytes = 384 << 10
	// maxReasoningEnvelopeBytes bounds the whole client-visible value: base64 of
	// one maximum payload plus the prefix, key id and MAC, rounded up.
	maxReasoningEnvelopeBytes = 512 << 10
	// reasoningProvenanceMaxTTL bounds how long a binding may stay valid.
	reasoningProvenanceMaxTTL = 30 * 24 * time.Hour
	// reasoningProvenanceClockSkew tolerates small clock differences when
	// rejecting implausibly future expiry timestamps.
	reasoningProvenanceClockSkew = 5 * time.Minute
	// reasoningProvenanceMinSecretBytes is the minimum signing secret length.
	reasoningProvenanceMinSecretBytes = 32
	// reasoningProvenanceDomain separates the signing subkey from the raw secret.
	reasoningProvenanceDomain = "rtr.provenance.binding.v1"
)

// Typed parse failures. Callers map these to bounded trace classes and never
// include the offending value or key material in diagnostics.
var (
	errEnvelopeEmpty        = errors.New("empty reasoning envelope")
	errEnvelopeTooLarge     = errors.New("reasoning envelope exceeds the size limit")
	errEnvelopeFormat       = errors.New("malformed reasoning envelope")
	errEnvelopeVersion      = errors.New("unsupported reasoning envelope version")
	errEnvelopeKeyID        = errors.New("reasoning envelope key id mismatch")
	errEnvelopeMAC          = errors.New("reasoning envelope authentication failed")
	errEnvelopeExpired      = errors.New("reasoning envelope expired")
	errEnvelopeExpiry       = errors.New("reasoning envelope expiry is invalid")
	errEnvelopeScope        = errors.New("reasoning envelope scope mismatch")
	errEnvelopeKind         = errors.New("reasoning envelope kind mismatch")
	errEnvelopePayloadLarge = errors.New("reasoning envelope payload exceeds the size limit")
	errEnvelopeOpaqueLarge  = errors.New("wrapped reasoning value exceeds the size limit")
	errEnvelopeKey          = errors.New("reasoning provenance key is invalid")
)

// reasoningEnvelopeKeyIDPattern keeps key ids log-safe and unambiguous in the
// dotted envelope format.
var reasoningEnvelopeKeyIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// reasoningKind names the opaque field an envelope wraps. Anthropic
// redacted_thinking carries its opaque payload in `data`, thinking in
// `signature`, and OpenAI Responses reasoning in `encrypted_content`.
type reasoningKind string

const (
	reasoningKindEncryptedContent reasoningKind = "openai-responses.encrypted_content"
	reasoningKindThinkingSig      reasoningKind = "anthropic.thinking.signature"
	reasoningKindRedactedThinking reasoningKind = "anthropic.redacted_thinking.data"
)

func (k reasoningKind) valid() bool {
	switch k {
	case reasoningKindEncryptedContent, reasoningKindThinkingSig, reasoningKindRedactedThinking:
		return true
	default:
		return false
	}
}

// reasoningScope is the origin tuple an opaque value is bound to. Scope values
// are configuration identifiers, never credentials.
type reasoningScope struct {
	Dialect  string
	Provider string
	KeyID    string
	Model    string
	Group    string
}

// digest returns a stable scope fingerprint. Fields are length-prefixed so no
// two different tuples can collide by concatenation.
func (s reasoningScope) digest() string {
	h := sha256.New()
	for _, field := range []string{s.Dialect, s.Provider, s.KeyID, s.Model, s.Group} {
		fmt.Fprintf(h, "%d:", len(field))
		h.Write([]byte(field))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// provenanceKey is the signing key for one rotation generation.
type provenanceKey struct {
	id     string
	secret []byte
}

// newReasoningProvenanceKey validates a configured key id and secret.
func newReasoningProvenanceKey(keyID, secret string) (provenanceKey, error) {
	keyID = strings.TrimSpace(keyID)
	if !reasoningEnvelopeKeyIDPattern.MatchString(keyID) {
		return provenanceKey{}, fmt.Errorf("%w: key id must match %s", errEnvelopeKey, reasoningEnvelopeKeyIDPattern)
	}
	if len(secret) < reasoningProvenanceMinSecretBytes {
		return provenanceKey{}, fmt.Errorf("%w: secret must be at least %d bytes", errEnvelopeKey, reasoningProvenanceMinSecretBytes)
	}
	if strings.TrimSpace(secret) != secret {
		return provenanceKey{}, fmt.Errorf("%w: secret must not have surrounding whitespace", errEnvelopeKey)
	}
	return provenanceKey{id: keyID, secret: []byte(secret)}, nil
}

// subkey derives the MAC key so the configured secret is never used directly.
func (k provenanceKey) subkey() []byte {
	mac := hmac.New(sha256.New, k.secret)
	mac.Write([]byte(reasoningProvenanceDomain))
	return mac.Sum(nil)
}

// sign returns the base64url MAC over the signed section.
func (k provenanceKey) sign(signed string) string {
	mac := hmac.New(sha256.New, k.subkey())
	mac.Write([]byte(signed))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type reasoningEnvelopePayload struct {
	Version int    `json:"v"`
	Scope   string `json:"scope"`
	Kind    string `json:"kind"`
	Expiry  int64  `json:"exp"`
	Value   string `json:"ct"`
}

// wrap binds an opaque provider value to a scope and expiry.
func (k provenanceKey) wrap(scope reasoningScope, kind reasoningKind, opaque string, now time.Time, ttl time.Duration) (string, error) {
	if len(k.secret) == 0 {
		return "", errEnvelopeKey
	}
	if !kind.valid() {
		return "", fmt.Errorf("%w: unknown kind", errEnvelopeFormat)
	}
	if opaque == "" {
		return "", fmt.Errorf("%w: empty value", errEnvelopeFormat)
	}
	if len(opaque) > maxReasoningOpaqueBytes {
		return "", errEnvelopeOpaqueLarge
	}
	if ttl <= 0 || ttl > reasoningProvenanceMaxTTL {
		return "", fmt.Errorf("%w: ttl out of range", errEnvelopeExpiry)
	}
	payload := reasoningEnvelopePayload{
		Version: reasoningProvenanceVersion,
		Scope:   scope.digest(),
		Kind:    string(kind),
		Expiry:  now.Add(ttl).Unix(),
		Value:   base64.RawURLEncoding.EncodeToString([]byte(opaque)),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if len(encoded) > maxReasoningPayloadBytes {
		return "", errEnvelopePayloadLarge
	}
	signed := reasoningEnvelopePrefix + "." + k.id + "." + base64.RawURLEncoding.EncodeToString(encoded)
	envelope := signed + "." + k.sign(signed)
	if len(envelope) > maxReasoningEnvelopeBytes {
		return "", errEnvelopeTooLarge
	}
	return envelope, nil
}

// parseReasoningEnvelope strictly parses and authenticates an envelope, then
// checks scope, kind and expiry. It never panics on hostile input.
func parseReasoningEnvelope(k provenanceKey, envelope string, scope reasoningScope, kind reasoningKind, now time.Time) (string, error) {
	if envelope == "" {
		return "", errEnvelopeEmpty
	}
	if len(envelope) > maxReasoningEnvelopeBytes {
		return "", errEnvelopeTooLarge
	}
	parts := strings.Split(envelope, ".")
	if len(parts) != 4 {
		return "", errEnvelopeFormat
	}
	if subtle.ConstantTimeCompare([]byte(parts[0]), []byte(reasoningEnvelopePrefix)) != 1 {
		return "", errEnvelopeFormat
	}
	if !reasoningEnvelopeKeyIDPattern.MatchString(parts[1]) {
		return "", errEnvelopeFormat
	}
	if parts[1] != k.id {
		return "", errEnvelopeKeyID
	}
	payloadRaw, err := decodeBoundedBase64(parts[2], maxReasoningPayloadBytes)
	if err != nil {
		return "", err
	}
	expected := k.sign(reasoningEnvelopePrefix + "." + parts[1] + "." + parts[2])
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[3])) != 1 {
		return "", errEnvelopeMAC
	}
	var payload reasoningEnvelopePayload
	decoder := json.NewDecoder(strings.NewReader(string(payloadRaw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return "", errEnvelopeFormat
	}
	if decoder.More() {
		return "", errEnvelopeFormat
	}
	if payload.Version != reasoningProvenanceVersion {
		return "", errEnvelopeVersion
	}
	if payload.Kind != string(kind) {
		return "", errEnvelopeKind
	}
	if payload.Scope != scope.digest() {
		return "", errEnvelopeScope
	}
	maxExpiry := now.Add(reasoningProvenanceMaxTTL + reasoningProvenanceClockSkew).Unix()
	if payload.Expiry <= 0 || payload.Expiry > maxExpiry {
		return "", errEnvelopeExpiry
	}
	if !now.Before(time.Unix(payload.Expiry, 0)) {
		return "", errEnvelopeExpired
	}
	if payload.Value == "" {
		return "", errEnvelopeFormat
	}
	value, err := decodeBoundedBase64(payload.Value, maxReasoningOpaqueBytes)
	if err != nil {
		return "", err
	}
	return string(value), nil
}

// decodeBoundedBase64 decodes raw URL base64, refusing padding, oversized
// input and any encoding that is not canonical.
func decodeBoundedBase64(value string, maxBytes int) ([]byte, error) {
	if value == "" || len(value) > maxBytes*4/3+8 {
		return nil, errEnvelopeFormat
	}
	if strings.ContainsAny(value, "+/=") {
		return nil, errEnvelopeFormat
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, errEnvelopeFormat
	}
	if len(decoded) > maxBytes {
		return nil, errEnvelopePayloadLarge
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errEnvelopeFormat
	}
	return decoded, nil
}

// reasoningProvenanceReason maps a parse failure to the bounded trace class
// used in diagnostics. It never includes the value or key material.
func reasoningProvenanceReason(err error) string {
	switch {
	case err == nil:
		return "verified"
	case errors.Is(err, errEnvelopeExpired):
		return "expired"
	case errors.Is(err, errEnvelopeScope):
		return "scope-mismatch"
	case errors.Is(err, errEnvelopeKind):
		return "kind-mismatch"
	case errors.Is(err, errEnvelopeKeyID):
		return "unknown-key"
	case errors.Is(err, errEnvelopeMAC):
		return "invalid-envelope"
	case errors.Is(err, errEnvelopeTooLarge), errors.Is(err, errEnvelopePayloadLarge), errors.Is(err, errEnvelopeOpaqueLarge):
		return "oversized"
	case errors.Is(err, errEnvelopeExpiry):
		return "invalid-expiry"
	default:
		return "invalid-envelope"
	}
}
