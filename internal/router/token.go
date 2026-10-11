// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"time"
)

const tokenPrefixName = "rtr_metrum"

// Caller token hash formats accepted by TokenGenerateOptions.HashFormat.
const (
	// TokenHashFormatPBKDF2SHA3 writes callers[].token_hash (the default).
	TokenHashFormatPBKDF2SHA3 = CallerTokenHashScheme
	// TokenHashFormatLegacySHA256 writes the deprecated callers[].token_sha256
	// for routers that predate token_hash support.
	TokenHashFormatLegacySHA256 = "legacy-sha256"
)

type TokenGenerateOptions struct {
	User        string
	OwnerUser   string
	Project     string
	Environment string
	KeySlug     string
	Allow       []string
	Reader      io.Reader
	Now         time.Time
	// HashFormat selects the stored verifier; empty means token_hash.
	HashFormat string
	// HashIterations overrides the token_hash PBKDF2 iteration count.
	HashIterations int
}

type GeneratedToken struct {
	Token       string       `json:"token"`
	TokenID     string       `json:"token_id"`
	TokenHash   string       `json:"token_hash,omitempty"`
	TokenSHA256 string       `json:"token_sha256,omitempty"`
	Caller      CallerConfig `json:"caller"`
}

func GenerateCallerToken(opts TokenGenerateOptions) (GeneratedToken, error) {
	user := slugify(opts.OwnerUser)
	if user == "" {
		user = slugify(opts.User)
	}
	project := slugify(opts.Project)
	environment := slugify(opts.Environment)
	if user == "" {
		return GeneratedToken{}, fmt.Errorf("user is required")
	}
	if project == "" {
		return GeneratedToken{}, fmt.Errorf("project is required")
	}
	if environment == "" {
		environment = "dev"
	}
	keySlug := slugify(opts.KeySlug)
	if keySlug == "" {
		now := opts.Now
		if now.IsZero() {
			now = time.Now().UTC()
		}
		keySlug = "k" + now.Format("20060102")
	}
	hashFormat := strings.ToLower(strings.TrimSpace(opts.HashFormat))
	switch hashFormat {
	case "", TokenHashFormatPBKDF2SHA3:
		hashFormat = TokenHashFormatPBKDF2SHA3
	case TokenHashFormatLegacySHA256:
	default:
		return GeneratedToken{}, fmt.Errorf("unsupported token hash format %q; use %s or %s", opts.HashFormat, TokenHashFormatPBKDF2SHA3, TokenHashFormatLegacySHA256)
	}
	reader := opts.Reader
	if reader == nil {
		reader = rand.Reader
	}
	secretRaw := make([]byte, 32)
	if _, err := io.ReadFull(reader, secretRaw); err != nil {
		return GeneratedToken{}, fmt.Errorf("generate token secret: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(secretRaw)
	tokenID := strings.Join([]string{tokenPrefixName, user, project, environment, keySlug}, "_")
	token := tokenID + "_" + secret
	allow := append([]string(nil), opts.Allow...)
	if len(allow) == 0 {
		return GeneratedToken{}, fmt.Errorf("at least one allowed model group is required")
	}
	callerID := strings.Join([]string{user, project, environment}, "-")
	caller := CallerConfig{
		ID:          callerID,
		OwnerUser:   user,
		Project:     project,
		Environment: environment,
		Status:      accountStatusActive,
		TokenID:     tokenID,
		Allow:       allow,
		Rate:        RateConfig{RPM: 120, TPM: 200000, Concurrent: 8},
		Quota:       QuotaConfig{Day: BudgetConfig{Requests: 5000, Tokens: 20000000}, Month: BudgetConfig{Tokens: 400000000}, SoftPct: 80},
		Key:         KeyConfig{LifetimeTokens: 2000000000, SoftPct: 90, OnExhaust: "disable"},
	}
	if hashFormat == TokenHashFormatLegacySHA256 {
		caller.TokenSHA256 = legacyCallerTokenSHA256Hex(token)
	} else {
		tokenHash, err := HashCallerToken(token, reader, opts.HashIterations)
		if err != nil {
			return GeneratedToken{}, err
		}
		caller.TokenHash = tokenHash
	}
	return GeneratedToken{Token: token, TokenID: tokenID, TokenHash: caller.TokenHash, TokenSHA256: caller.TokenSHA256, Caller: caller}, nil
}

// CallerTokenIDFromToken returns the public token_id of a structured
// rtr_metrum_ caller token, using the publicTokenID rule. ok is false when
// the token lacks the "<token_id>_<secret>" shape that token_hash lookup
// requires; such a token cannot be converted to token_hash in place.
func CallerTokenIDFromToken(token string) (tokenID string, ok bool) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, tokenPrefixName+"_") {
		return "", false
	}
	tokenID = publicTokenID(token)
	if tokenID == token || len(token) <= len(tokenID)+1 || !strings.HasPrefix(token, tokenID+"_") {
		return "", false
	}
	return tokenID, true
}

func publicTokenID(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, tokenPrefixName+"_") {
		return v
	}
	parts := strings.Split(v, "_")
	if len(parts) <= 6 {
		return v
	}
	return strings.Join(parts[:6], "_")
}

func slugify(in string) string {
	in = strings.ToLower(strings.TrimSpace(in))
	var b strings.Builder
	lastSep := false
	for _, r := range in {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSep = false
		case r == '-' || r == '_' || r == '.' || r == ' ' || r == '/':
			if b.Len() > 0 && !lastSep {
				b.WriteByte('-')
				lastSep = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	return out
}

func callerUser(c CallerConfig) string {
	if v := slugify(c.OwnerUser); v != "" {
		return v
	}
	if v := slugify(c.User); v != "" {
		return v
	}
	if v := slugify(c.ID); v != "" {
		return v
	}
	return "unknown"
}

func callerProject(c CallerConfig) string {
	if v := slugify(c.Project); v != "" {
		return v
	}
	return "unknown"
}

func callerEnvironment(c CallerConfig) string {
	if v := slugify(c.Environment); v != "" {
		return v
	}
	return "unknown"
}
