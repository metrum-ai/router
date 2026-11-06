// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

type adminMachineJWTSubject struct{ Claim, Value, Subject string }
type adminMachineJWTConfig struct {
	// Test-only hook; not a configuration key. Runtime construction leaves false.
	allowInsecureLoopback  bool
	Issuer, Audience, Kind string
	Algorithms             []string
	MaxLifetime, ClockSkew time.Duration
	Subjects               []adminMachineJWTSubject
	// Exact additional host[:port] authorities; HTTPS port 443 is implicit.
	// No wildcard/URL entries. The issuer's own authority is always allowed.
	AllowedJWKSHosts []string
	// Per-issuer opt-in for private/special-use egress; HTTPS remains required.
	AllowPrivateNetwork bool
	// Provider compatibility opt-out for absent or generic JWT typ only.
	// Requires a dedicated admin audience; explicit id+jwt remains forbidden.
	AllowLegacyTokenType bool
}

type adminMachineJWTAuthenticator struct {
	cfg                                      adminMachineJWTConfig
	algorithms                               []jose.SignatureAlgorithm
	client                                   *http.Client
	jwksURL                                  string
	mu                                       sync.Mutex
	refreshing                               chan struct{}
	keys                                     jose.JSONWebKeySet
	expires, lastUnknownRefresh, lastAttempt time.Time
}

const machineJWKSTTL = 5 * time.Minute
const machineJWKSRefreshInterval = 30 * time.Second

func machineIssuerEndpoint(raw string, testLoopback bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return testLoopback && u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())
}

func newAdminMachineJWTAuthenticator(ctx context.Context, cfg adminMachineJWTConfig, client *http.Client) (*adminMachineJWTAuthenticator, error) {
	cfg.allowInsecureLoopback = cfg.allowInsecureLoopback && client != nil
	if cfg.Issuer == "" || cfg.Audience == "" || len(cfg.Subjects) == 0 || !machineIssuerEndpoint(cfg.Issuer, cfg.allowInsecureLoopback) {
		return nil, errors.New("admin JWT issuer, audience or subjects invalid")
	}
	issuerURL, _ := url.Parse(cfg.Issuer) // parsed by machineIssuerEndpoint above
	if issuerURL.RawQuery != "" {
		return nil, errors.New("admin JWT issuer query forbidden")
	}
	cfg.AllowedJWKSHosts = append([]string(nil), cfg.AllowedJWKSHosts...)
	if len(cfg.AllowedJWKSHosts) > 16 {
		return nil, errors.New("admin JWT JWKS host allowlist too large")
	}
	for _, host := range cfg.AllowedJWKSHosts {
		if _, ok := machineJWTAllowedHost(host); !ok {
			return nil, errors.New("admin JWT JWKS host allowlist invalid")
		}
	}
	if cfg.MaxLifetime == 0 {
		cfg.MaxLifetime = time.Hour
	}
	if cfg.ClockSkew == 0 {
		cfg.ClockSkew = 30 * time.Second
	}
	if cfg.MaxLifetime < 0 || cfg.ClockSkew < 0 || cfg.ClockSkew > time.Minute {
		return nil, errors.New("admin JWT lifetime or skew invalid")
	}
	if cfg.Kind == "" {
		cfg.Kind = "oauth"
	}
	if cfg.Kind != "oauth" && cfg.Kind != "federated" {
		return nil, errors.New("admin JWT kind invalid")
	}
	cfg.Subjects = append([]adminMachineJWTSubject(nil), cfg.Subjects...)
	seen := map[string]string{}
	for _, mapping := range cfg.Subjects {
		if (mapping.Claim != "sub" && mapping.Claim != "client_id") || mapping.Value == "" || strings.TrimSpace(mapping.Subject) == "" {
			return nil, errors.New("admin JWT immutable subject mapping invalid")
		}
		id := mapping.Claim + "\x00" + mapping.Value
		if previous := seen[id]; previous != "" && previous != mapping.Subject {
			return nil, errors.New("admin JWT subject mapping ambiguous")
		}
		seen[id] = mapping.Subject
	}
	if len(cfg.Algorithms) == 0 {
		cfg.Algorithms = []string{"RS256", "ES256"}
	}
	a := &adminMachineJWTAuthenticator{cfg: cfg}
	for _, alg := range cfg.Algorithms {
		switch alg {
		case "RS256", "ES256":
		default:
			return nil, errors.New("admin JWT asymmetric algorithm required")
		}
		a.algorithms = append(a.algorithms, jose.SignatureAlgorithm(alg))
	}
	boundedClient, err := newMachineJWTHTTPClient(cfg, client)
	if err != nil {
		return nil, err
	}
	a.client = boundedClient
	var discovery struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := a.fetchJSON(ctx, strings.TrimRight(cfg.Issuer, "/")+"/.well-known/openid-configuration", &discovery); err != nil {
		return nil, err
	}
	if discovery.Issuer != cfg.Issuer || !machineJWTEndpointAllowed(cfg, discovery.JWKSURI) {
		return nil, errors.New("admin JWT discovery invalid")
	}
	a.jwksURL = discovery.JWKSURI
	keys, err := a.fetchKeys(ctx)
	if err != nil {
		return nil, err
	}
	a.keys = keys
	a.expires = time.Now().Add(machineJWKSTTL)
	a.lastAttempt = time.Now()
	return a, nil
}

func (a *adminMachineJWTAuthenticator) fetchJSON(ctx context.Context, endpoint string, dest any) error {
	if !machineJWTEndpointAllowed(a.cfg, endpoint) {
		return errors.New("admin JWT endpoint forbidden")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("admin JWT discovery request invalid")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return errors.New("admin JWT discovery unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("admin JWT discovery rejected")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("admin JWT discovery oversized or unreadable")
	}
	if json.Unmarshal(data, dest) != nil {
		return errors.New("admin JWT discovery malformed")
	}
	return nil
}

// fetchKeys performs bounded I/O without holding the cache lock.
func (a *adminMachineJWTAuthenticator) fetchKeys(ctx context.Context) (jose.JSONWebKeySet, error) {
	var keys jose.JSONWebKeySet
	if err := a.fetchJSON(ctx, a.jwksURL, &keys); err != nil {
		return keys, err
	}
	if len(keys.Keys) == 0 || len(keys.Keys) > 128 {
		return jose.JSONWebKeySet{}, errors.New("admin JWKS key count invalid")
	}
	return keys, nil
}

func machineJWTKeyMatches(key jose.JSONWebKey, alg string) bool {
	if !key.Valid() || !key.IsPublic() || (key.Use != "" && key.Use != "sig") || (key.Algorithm != "" && key.Algorithm != alg) {
		return false
	}
	switch k := key.Key.(type) {
	case *rsa.PublicKey:
		return k.N.BitLen() >= 2048 && k.N.BitLen() <= 8192 && alg == "RS256"
	case *ecdsa.PublicKey:
		return alg == "ES256" && k.Curve.Params().Name == "P-256"
	}
	return false
}

func (a *adminMachineJWTAuthenticator) key(ctx context.Context, kid, alg string) (jose.JSONWebKey, error) {
	for {
		if ctx.Err() != nil {
			return jose.JSONWebKey{}, ErrInvalidCredential
		}
		a.mu.Lock()
		now := time.Now()
		fresh := now.Before(a.expires)
		matching := a.keys.Key(kid)
		if fresh && len(matching) > 0 {
			a.mu.Unlock()
			if len(matching) != 1 || !machineJWTKeyMatches(matching[0], alg) {
				return jose.JSONWebKey{}, ErrInvalidCredential
			}
			return matching[0], nil
		}
		// Known keys in a fresh cache never wait for unknown-key refresh I/O.
		// Requests needing refreshed trust coalesce and can cancel independently.
		if done := a.refreshing; done != nil {
			a.mu.Unlock()
			select {
			case <-ctx.Done():
				return jose.JSONWebKey{}, ErrInvalidCredential
			case <-done:
				continue
			}
		}
		if !fresh && now.Sub(a.lastAttempt) < machineJWKSRefreshInterval || fresh && now.Sub(a.lastUnknownRefresh) < machineJWKSRefreshInterval {
			a.mu.Unlock()
			return jose.JSONWebKey{}, ErrInvalidCredential
		}
		done := make(chan struct{})
		a.refreshing = done
		a.lastAttempt = now
		if len(matching) == 0 {
			a.lastUnknownRefresh = now
		}
		a.mu.Unlock()
		// The initiating request does not own this shared refresh lifetime.
		// A disconnect must not consume the refresh slot while cancelling I/O
		// needed by other requests. All waiters, including the initiator, can
		// independently cancel; the shared work has its own bounded timeout.
		go func() {
			refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			keys, err := a.fetchKeys(refreshCtx)
			a.mu.Lock()
			if err == nil {
				a.keys = keys
				a.expires = time.Now().Add(machineJWKSTTL)
			}
			a.refreshing = nil
			close(done)
			a.mu.Unlock()
		}()
		select {
		case <-ctx.Done():
			return jose.JSONWebKey{}, ErrInvalidCredential
		case <-done:
		}
	}
}

func (a *adminMachineJWTAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if ctx.Err() != nil || r == nil {
		return Principal{}, ErrInvalidCredential
	}
	raw := bearerTokenFromRequest(r)
	if raw == "" || strings.HasPrefix(raw, adminAPIKeyPrefix) {
		return Principal{}, ErrNoCredential
	}
	if len(raw) > maxMachineCredentialBytes {
		return Principal{}, ErrInvalidCredential
	}
	token, err := jwt.ParseSigned(raw, a.algorithms)
	if err != nil || len(token.Headers) != 1 || token.Headers[0].KeyID == "" {
		return Principal{}, ErrInvalidCredential
	}
	header := token.Headers[0]
	typeValue, present := header.ExtraHeaders[jose.HeaderKey("typ")]
	tokenType, stringType := typeValue.(string)
	if present && !stringType {
		return Principal{}, ErrInvalidCredential
	}
	switch strings.ToLower(tokenType) {
	case "at+jwt", "application/at+jwt":
	case "", "jwt":
		if !a.cfg.AllowLegacyTokenType {
			return Principal{}, ErrInvalidCredential
		}
	default:
		return Principal{}, ErrInvalidCredential
	}
	key, err := a.key(ctx, header.KeyID, header.Algorithm)
	if err != nil {
		return Principal{}, ErrInvalidCredential
	}
	var claims struct {
		jwt.Claims
		ClientID     string          `json:"client_id"`
		Confirmation json.RawMessage `json:"cnf"`
	}
	if token.Claims(key.Key, &claims) != nil {
		return Principal{}, ErrInvalidCredential
	}
	now := time.Now()
	skew := a.cfg.ClockSkew
	if claims.Issuer != a.cfg.Issuer || !claims.Audience.Contains(a.cfg.Audience) || claims.Expiry == nil || claims.IssuedAt == nil || len(claims.Confirmation) > 0 {
		return Principal{}, ErrInvalidCredential
	}
	exp, iat := claims.Expiry.Time(), claims.IssuedAt.Time()
	if !exp.After(iat) || exp.Sub(iat) > a.cfg.MaxLifetime || !now.Before(exp.Add(skew)) || iat.After(now.Add(skew)) || claims.NotBefore != nil && claims.NotBefore.Time().After(now.Add(skew)) {
		return Principal{}, ErrInvalidCredential
	}
	subject, credentialID := "", ""
	for _, mapping := range a.cfg.Subjects {
		value := claims.Subject
		if mapping.Claim == "client_id" {
			value = claims.ClientID
		}
		if value != mapping.Value {
			continue
		}
		if subject != "" && subject != mapping.Subject {
			return Principal{}, ErrInvalidCredential
		}
		subject = mapping.Subject
		credentialID = mapping.Claim + ":" + mapping.Value
	}
	if subject == "" || ctx.Err() != nil {
		return Principal{}, ErrInvalidCredential
	}
	return Principal{Subject: subject, Kind: a.cfg.Kind, Issuer: a.cfg.Issuer, CredentialID: credentialID}, nil
}
