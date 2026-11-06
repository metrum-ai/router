// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// ErrSenderConstraintFailed never carries token, proof, or verifier material.
var ErrSenderConstraintFailed = errors.New("sender_constraint_failed")

// adminDPoPValidator implements the RFC 9449 protected-resource proof checks.
// Replay state is node-local: across replicas, the same proof can be accepted
// once per node during proofMaxAge. No nonce challenge is issued in this profile.
// Future iat is rejected, bounding replay validity by proofMaxAge without skew.
type adminDPoPValidator struct {
	externalBase string
	proofMaxAge  time.Duration
	capacity     int
	mu           sync.Mutex
	seen         map[[32]byte]time.Time
	now          func() time.Time
}

func newAdminDPoPValidator(externalBase string, maxAge time.Duration, capacity int) (*adminDPoPValidator, error) {
	u, err := url.Parse(externalBase)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || maxAge <= 0 || maxAge > 5*time.Minute || capacity <= 0 || capacity > 100000 {
		return nil, errors.New("admin DPoP configuration invalid")
	}
	base, err := normalizeAdminDPoPURI(externalBase)
	if err != nil {
		return nil, errors.New("admin DPoP external URL invalid")
	}
	return &adminDPoPValidator{externalBase: strings.TrimRight(base, "/"), proofMaxAge: maxAge, capacity: capacity, seen: make(map[[32]byte]time.Time), now: time.Now}, nil
}

// normalizeAdminDPoPURI applies scheme, host, default-port and percent-encoded
// unreserved normalization. Reserved path characters remain escaped. Query and
// fragment are excluded per RFC 9449. Dot-segment paths are compared literally
// rather than broadening equivalence across potentially distinct routed paths.
func normalizeAdminDPoPURI(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" || u.User != nil {
		return "", ErrSenderConstraintFailed
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", ErrSenderConstraintFailed
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" && !(scheme == "https" && port == "443" || scheme == "http" && port == "80") {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	var normalized strings.Builder
	const hex = "0123456789ABCDEF"
	for i := 0; i < len(path); i++ {
		if path[i] != '%' {
			normalized.WriteByte(path[i])
			continue
		}
		if i+2 >= len(path) {
			return "", ErrSenderConstraintFailed
		}
		n, err := strconv.ParseUint(path[i+1:i+3], 16, 8)
		if err != nil {
			return "", ErrSenderConstraintFailed
		}
		b := byte(n)
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("-._~", rune(b)) {
			normalized.WriteByte(b)
		} else {
			normalized.WriteByte('%')
			normalized.WriteByte(hex[b>>4])
			normalized.WriteByte(hex[b&15])
		}
		i += 2
	}
	return scheme + "://" + host + normalized.String(), nil
}

func dpopProofKeyValid(key *jose.JSONWebKey, alg string) bool {
	if key == nil || !key.Valid() || !key.IsPublic() || (key.Use != "" && key.Use != "sig") || (key.Algorithm != "" && key.Algorithm != alg) {
		return false
	}
	switch k := key.Key.(type) {
	case *ecdsa.PublicKey:
		return alg == "ES256" && k.Curve.Params().Name == "P-256"
	case *rsa.PublicKey:
		return alg == "RS256" && k.N.BitLen() >= 2048 && k.N.BitLen() <= 8192
	}
	return false
}

// Validate is called only after access-token signature/claims validation and
// uses that token's cnf.jkt. externalBase is trusted configuration, never Host
// or Forwarded. Its optional path prefix is prepended to the backend path.
func (v *adminDPoPValidator) Validate(ctx context.Context, r *http.Request, accessToken, jkt string) error {
	if ctx.Err() != nil || r == nil || r.URL == nil || accessToken == "" || len(accessToken) > 16*1024 {
		return ErrSenderConstraintFailed
	}
	var authorization, proof string
	authCount, proofCount := 0, 0
	for name, values := range r.Header {
		if strings.EqualFold(name, "Authorization") {
			authCount += len(values)
			if len(values) == 1 {
				authorization = values[0]
			}
		}
		if strings.EqualFold(name, "DPoP") {
			proofCount += len(values)
			if len(values) == 1 {
				proof = values[0]
			}
		}
	}
	fields := strings.Fields(authorization)
	if authCount != 1 || proofCount != 1 || len(fields) != 2 || !strings.EqualFold(fields[0], "DPoP") || fields[1] != accessToken || len(proof) == 0 || len(proof) > 16*1024 {
		return ErrSenderConstraintFailed
	}
	token, err := jwt.ParseSigned(proof, []jose.SignatureAlgorithm{jose.ES256, jose.RS256})
	if err != nil || len(token.Headers) != 1 {
		return ErrSenderConstraintFailed
	}
	header := token.Headers[0]
	if header.ExtraHeaders["typ"] != "dpop+jwt" || !dpopProofKeyValid(header.JSONWebKey, header.Algorithm) {
		return ErrSenderConstraintFailed
	}
	thumb, err := header.JSONWebKey.Thumbprint(crypto.SHA256)
	if err != nil || subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(thumb)), []byte(jkt)) != 1 {
		return ErrSenderConstraintFailed
	}
	var claims struct {
		ID        string           `json:"jti"`
		IssuedAt  *jwt.NumericDate `json:"iat"`
		Method    string           `json:"htm"`
		URI       string           `json:"htu"`
		TokenHash string           `json:"ath"`
	}
	if token.Claims(header.JSONWebKey.Key, &claims) != nil || claims.ID == "" || len(claims.ID) > 256 || claims.IssuedAt == nil || claims.Method != r.Method {
		return ErrSenderConstraintFailed
	}
	proofURI, err := normalizeAdminDPoPURI(claims.URI)
	if err != nil {
		return ErrSenderConstraintFailed
	}
	requestPath := r.URL.EscapedPath()
	if requestPath == "" {
		requestPath = "/"
	}
	requestURI, err := normalizeAdminDPoPURI(v.externalBase + requestPath)
	if err != nil || proofURI != requestURI {
		return ErrSenderConstraintFailed
	}
	tokenHash := sha256.Sum256([]byte(accessToken))
	if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(tokenHash[:])), []byte(claims.TokenHash)) != 1 {
		return ErrSenderConstraintFailed
	}
	now := v.now()
	issuedAt := claims.IssuedAt.Time()
	expires := issuedAt.Add(v.proofMaxAge)
	if issuedAt.After(now) || !now.Before(expires) || ctx.Err() != nil {
		return ErrSenderConstraintFailed
	}
	// Claim only after every cryptographic and request-binding check succeeds.
	// Hash jti to bound per-entry memory; never evict an unexpired replay record.
	replayKey := sha256.Sum256([]byte(jkt + "\x00" + claims.ID))
	v.mu.Lock()
	defer v.mu.Unlock()
	now = v.now()
	if issuedAt.After(now) || !now.Before(expires) || ctx.Err() != nil {
		return ErrSenderConstraintFailed
	}
	if previous, ok := v.seen[replayKey]; ok && now.Before(previous) {
		return ErrSenderConstraintFailed
	}
	delete(v.seen, replayKey)
	if len(v.seen) >= v.capacity {
		for key, expiry := range v.seen {
			if !now.Before(expiry) {
				delete(v.seen, key)
			}
		}
	}
	if len(v.seen) >= v.capacity {
		return ErrSenderConstraintFailed
	}
	v.seen[replayKey] = expires
	return nil
}
