// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Caller token verifiers (Metrum AI Router).
//
// Caller bearer tokens are verified against a self-describing, salted,
// computationally expensive verifier stored in callers[].token_hash:
//
//	pbkdf2-sha3-512:i=<iterations>:<salt>:<derived-key>
//
// salt and derived-key use unpadded base64url, which never contains ":".
// The separator is ":" rather than the PHC-style "$" because config files are
// passed through os.ExpandEnv, which would rewrite "$name" sequences. The derivation is PBKDF2
// (crypto/pbkdf2) over HMAC-SHA3-512 (crypto/sha3), both pure Go. SHA-3 is
// the Keccak family that ML-KEM and ML-DSA build on, and a 512-bit output
// keeps 256-bit security against Grover-style search.
//
// The expensive derivation runs once per process per caller token. After a
// token verifies, the runtime remembers a keyed digest of it
// (HMAC-SHA3-512 under a per-process random key that is never persisted or
// logged), so later requests cost one HMAC and a constant-time comparison.
// See callerAuthIndex in caller_auth.go.
//
// The deprecated callers[].token_sha256 field (unsalted SHA-256 hex) still
// works for existing deployments. Its only hashing call is
// legacyCallerTokenSHA256Hex, kept isolated so it can be deleted once
// token_sha256 support is removed.

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha3"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	// CallerTokenHashScheme names the only supported token_hash algorithm.
	CallerTokenHashScheme = "pbkdf2-sha3-512"
	// CallerTokenHashDefaultIterations balances first-use verification cost
	// (~40 ms in pure Go on a current x86-64 core) against offline guessing.
	// Caller tokens carry 256 bits of randomness, so the work factor is
	// defence in depth rather than the primary protection.
	CallerTokenHashDefaultIterations = 20000
	callerTokenHashMinIterations     = 10000
	// The upper bound keeps a typo in config from turning first-use
	// verification into a CPU denial of service.
	callerTokenHashMaxIterations = 1000000
	callerTokenHashSaltBytes     = 32
	callerTokenHashMinSaltBytes  = 16
	callerTokenHashMaxSaltBytes  = 64
	callerTokenHashKeyBytes      = 64
	// maxCallerTokenBytes bounds work done on an unauthenticated header.
	maxCallerTokenBytes = 4096
)

// callerTokenVerifier is a parsed token_hash value.
type callerTokenVerifier struct {
	iterations int
	salt       []byte
	key        []byte
}

var callerTokenB64 = base64.RawURLEncoding

// parseCallerTokenHash validates and parses a token_hash string. Errors never
// include the input, so a misplaced raw token cannot leak through logs.
func parseCallerTokenHash(encoded string) (callerTokenVerifier, error) {
	parts := strings.Split(strings.TrimSpace(encoded), ":")
	if len(parts) != 4 {
		return callerTokenVerifier{}, fmt.Errorf("must have the form %s:i=<iterations>:<salt>:<key>", CallerTokenHashScheme)
	}
	if parts[0] != CallerTokenHashScheme {
		return callerTokenVerifier{}, fmt.Errorf("unsupported algorithm; want %s", CallerTokenHashScheme)
	}
	iterText, ok := strings.CutPrefix(parts[1], "i=")
	if !ok {
		return callerTokenVerifier{}, errors.New("missing i=<iterations> parameter")
	}
	iterations, err := strconv.Atoi(iterText)
	if err != nil || strconv.Itoa(iterations) != iterText {
		return callerTokenVerifier{}, errors.New("iterations must be a decimal integer")
	}
	if iterations < callerTokenHashMinIterations || iterations > callerTokenHashMaxIterations {
		return callerTokenVerifier{}, fmt.Errorf("iterations must be between %d and %d", callerTokenHashMinIterations, callerTokenHashMaxIterations)
	}
	salt, err := callerTokenB64.DecodeString(parts[2])
	if err != nil {
		return callerTokenVerifier{}, errors.New("salt must be unpadded base64url")
	}
	if len(salt) < callerTokenHashMinSaltBytes || len(salt) > callerTokenHashMaxSaltBytes {
		return callerTokenVerifier{}, fmt.Errorf("salt must be %d to %d bytes", callerTokenHashMinSaltBytes, callerTokenHashMaxSaltBytes)
	}
	key, err := callerTokenB64.DecodeString(parts[3])
	if err != nil {
		return callerTokenVerifier{}, errors.New("key must be unpadded base64url")
	}
	if len(key) != callerTokenHashKeyBytes {
		return callerTokenVerifier{}, fmt.Errorf("key must be %d bytes", callerTokenHashKeyBytes)
	}
	return callerTokenVerifier{iterations: iterations, salt: salt, key: key}, nil
}

// String renders the canonical token_hash form.
func (v callerTokenVerifier) String() string {
	return CallerTokenHashScheme + ":i=" + strconv.Itoa(v.iterations) + ":" + callerTokenB64.EncodeToString(v.salt) + ":" + callerTokenB64.EncodeToString(v.key)
}

// derive runs the expensive PBKDF2-HMAC-SHA3-512 derivation.
func (v callerTokenVerifier) derive(token string) ([]byte, error) {
	return pbkdf2.Key(sha3.New512, token, v.salt, v.iterations, len(v.key))
}

// matches reports whether token derives to the stored key, comparing in
// constant time.
func (v callerTokenVerifier) matches(token string) bool {
	if token == "" || len(token) > maxCallerTokenBytes {
		return false
	}
	derived, err := v.derive(token)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(derived, v.key) == 1
}

// HashCallerToken returns a new token_hash value for token. A nil reader uses
// crypto/rand; iterations <= 0 uses CallerTokenHashDefaultIterations.
func HashCallerToken(token string, reader io.Reader, iterations int) (string, error) {
	if token == "" || len(token) > maxCallerTokenBytes {
		return "", errors.New("caller token is empty or too long")
	}
	if iterations <= 0 {
		iterations = CallerTokenHashDefaultIterations
	}
	if iterations < callerTokenHashMinIterations || iterations > callerTokenHashMaxIterations {
		return "", fmt.Errorf("iterations must be between %d and %d", callerTokenHashMinIterations, callerTokenHashMaxIterations)
	}
	if reader == nil {
		reader = rand.Reader
	}
	salt := make([]byte, callerTokenHashSaltBytes)
	if _, err := io.ReadFull(reader, salt); err != nil {
		return "", fmt.Errorf("generate token hash salt: %w", err)
	}
	v := callerTokenVerifier{iterations: iterations, salt: salt, key: make([]byte, callerTokenHashKeyBytes)}
	key, err := v.derive(token)
	if err != nil {
		return "", fmt.Errorf("derive token hash: %w", err)
	}
	v.key = key
	return v.String(), nil
}

// VerifyCallerTokenHash checks token against a token_hash value. It returns an
// error only when encoded is malformed.
func VerifyCallerTokenHash(encoded, token string) (bool, error) {
	v, err := parseCallerTokenHash(encoded)
	if err != nil {
		return false, err
	}
	return v.matches(token), nil
}

// ValidateCallerTokenHash reports whether encoded is a well-formed token_hash.
func ValidateCallerTokenHash(encoded string) error {
	_, err := parseCallerTokenHash(encoded)
	return err
}

// isCallerTokenHashValue reports whether a stored verifier string uses the
// self-describing token_hash format rather than legacy SHA-256 hex. The
// config control-plane database keeps both forms in one column.
func isCallerTokenHashValue(stored string) bool {
	return strings.HasPrefix(strings.TrimSpace(stored), CallerTokenHashScheme+":")
}

// storedCallerTokenVerifier returns the value persisted in the config
// control-plane router_config_callers.token_sha256 column. The column predates
// token_hash; its values are told apart by the self-describing prefix, which
// avoids a schema migration.
func storedCallerTokenVerifier(caller CallerConfig) string {
	if tokenHash := strings.TrimSpace(caller.TokenHash); tokenHash != "" {
		return tokenHash
	}
	return caller.TokenSHA256
}

// splitStoredCallerTokenVerifier reverses storedCallerTokenVerifier.
func splitStoredCallerTokenVerifier(stored string) (tokenHash, legacySHA256 string) {
	if isCallerTokenHashValue(stored) {
		return strings.TrimSpace(stored), ""
	}
	return "", stored
}

// legacyCallerTokenSHA256Hex is the single remaining unsalted SHA-256 over a
// caller token. It exists only to verify the deprecated
// callers[].token_sha256 field during the migration window and to let
// operators generate that legacy form explicitly. Its static-analysis
// finding (go/weak-sensitive-data-hashing) is expected while token_sha256 is
// supported; delete this function when that field is removed.
func legacyCallerTokenSHA256Hex(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
