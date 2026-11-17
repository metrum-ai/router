// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	tokenExchangeGrantType = "urn:ietf:params:oauth:grant-type:token-exchange"
	jwtSubjectTokenType    = "urn:ietf:params:oauth:token-type:jwt"
	missionTokenIssuer     = "metrum-ai-router"
	missionTokenAudience   = "metrum-ai-router"
	maxTaskIDBytes         = 128
	maxTokenExchangeGrants = 4096
	tokenExchangeRPM       = 30
)

type tokenExchangeGrant struct {
	config TokenExchangeSubjectGrant
	id     string
	key    string
}

type tokenExchangeIssuer struct {
	config    TokenExchangeIssuerConfig
	profile   adminMachineJWTConfig
	taskClaim string
	client    *http.Client
	mu        sync.Mutex
	auth      *adminMachineJWTAuthenticator
	retryAt   time.Time
	backoff   time.Duration
	grants    map[string]tokenExchangeGrant
}

type tokenExchangeRuntime struct {
	key      []byte
	ttl      time.Duration
	quota    *quotaStore
	client   *http.Client
	issuers  map[string]*tokenExchangeIssuer
	grants   map[string]tokenExchangeGrant
	mu       sync.RWMutex
	budgetMu sync.Mutex
	budgets  map[string]*missionBudget
	rateMu   sync.Mutex
	rates    map[string][]time.Time
	callers  map[string]*callerRuntime
}

type missionBudget struct {
	usedTokens     int
	usedSpend      float64
	reservedTokens int
	reservedSpend  float64
}

type missionTokenClaims struct {
	jwt.Claims
	CallerID     string   `json:"caller_id"`
	Groups       []string `json:"groups"`
	MaxSpendUSD  float64  `json:"max_spend_usd"`
	MaxTokens    int      `json:"max_tokens"`
	AllowedTools []string `json:"allowed_tools"`
	TaskID       string   `json:"task_id"`
	HumanOwner   string   `json:"human_owner"`
	GrantID      string   `json:"grant_id"`
}

func validateTokenExchange(cfg *Config) error {
	tx := cfg.Security.TokenExchange
	if !tx.Enabled {
		if tx.SigningKeyEnv != "" || len(tx.Issuers) != 0 {
			return fmt.Errorf("security.token_exchange requires enabled: true")
		}
		return nil
	}
	if !validEnvName(tx.SigningKeyEnv) {
		return fmt.Errorf("security.token_exchange.signing_key_env must name an environment variable")
	}
	ttl, err := time.ParseDuration(tx.DefaultTTL)
	if err != nil || ttl < time.Second || ttl > 15*time.Minute {
		return fmt.Errorf("security.token_exchange.default_ttl must be between 1s and 15m")
	}
	if len(tx.Issuers) == 0 || len(tx.Issuers) > 32 {
		return fmt.Errorf("security.token_exchange.issuers must contain 1 to 32 issuers")
	}
	callers := make(map[string]CallerConfig, len(cfg.Callers))
	for _, caller := range cfg.Callers {
		callers[caller.ID] = caller
	}
	seenIssuers := make(map[string]bool)
	grantCount := 0
	for _, issuer := range tx.Issuers {
		if !machineIssuerEndpoint(issuer.IssuerURL, false) || issuer.Audience == "" || len(issuer.Subjects) == 0 {
			return fmt.Errorf("security.token_exchange issuer or audience invalid")
		}
		if seenIssuers[issuer.IssuerURL] {
			return fmt.Errorf("security.token_exchange issuer duplicated")
		}
		if issuer.TaskClaim != "" && !validJWTClaimPath(issuer.TaskClaim) {
			return fmt.Errorf("security.token_exchange issuer task_claim is invalid")
		}
		seenIssuers[issuer.IssuerURL] = true
		profile := adminMachineJWTConfig{Issuer: issuer.IssuerURL, Audience: issuer.Audience, Algorithms: issuer.Algorithms, AllowedJWKSHosts: issuer.AllowedJWKSHosts, AllowPrivateNetwork: issuer.AllowPrivateNetwork}
		seenSubjects := map[string]bool{}
		for _, grant := range issuer.Subjects {
			grantCount++
			if grantCount > maxTokenExchangeGrants {
				return fmt.Errorf("security.token_exchange supports at most %d subject grants", maxTokenExchangeGrants)
			}
			if (grant.Claim != "sub" && grant.Claim != "client_id") || grant.Value == "" || len(grant.Value) > 1024 || strings.TrimSpace(grant.HumanOwner) == "" || len(grant.HumanOwner) > 256 || strings.ContainsAny(grant.HumanOwner, "\r\n") || len(grant.AllowedGroups) == 0 || !finitePositive(grant.MaxSpendUSD) || grant.MaxTokens < 1 || grant.MaxTokens > 1_000_000 || (grant.TaskID != "" && !validMissionTaskID(grant.TaskID)) {
				return fmt.Errorf("security.token_exchange issuer subject grant invalid")
			}
			key := grant.Claim + "\x00" + grant.Value
			if seenSubjects[key] {
				return fmt.Errorf("security.token_exchange subject grant duplicated")
			}
			seenSubjects[key] = true
			caller, ok := callers[grant.CallerID]
			if !ok {
				return fmt.Errorf("security.token_exchange grant references unknown caller")
			}
			for _, group := range grant.AllowedGroups {
				if _, exists := cfg.Models[group]; !exists || !stringSliceContains(caller.Allow, group) {
					return fmt.Errorf("security.token_exchange grant group is not allowed for its caller")
				}
			}
			for _, tool := range grant.AllowedTools {
				if strings.TrimSpace(tool) == "" || strings.ContainsAny(tool, "\r\n") {
					return fmt.Errorf("security.token_exchange allowed_tools entry invalid")
				}
			}
			profile.Subjects = append(profile.Subjects, adminMachineJWTSubject{Claim: grant.Claim, Value: grant.Value, Subject: grant.CallerID})
		}
		if _, err := normalizeTokenExchangeJWTProfile(profile); err != nil {
			return fmt.Errorf("security.token_exchange issuer configuration invalid: %w", err)
		}
	}
	return nil
}

func validJWTClaimPath(path string) bool {
	if path == "" || len(path) > 256 {
		return false
	}
	parts := strings.Split(path, ".")
	if len(parts) > 16 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			ch := part[i]
			if !(ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
				return false
			}
		}
	}
	return true
}

func normalizeTokenExchangeJWTProfile(cfg adminMachineJWTConfig) (adminMachineJWTConfig, error) {
	if err := normalizeMachineJWTJWKSSettings(&cfg); err != nil {
		return cfg, err
	}
	if len(cfg.Algorithms) == 0 {
		cfg.Algorithms = []string{"RS256", "ES256"}
	}
	for _, alg := range cfg.Algorithms {
		if alg != "RS256" && alg != "ES256" {
			return cfg, errors.New("asymmetric algorithm required")
		}
	}
	if len(cfg.AllowedJWKSHosts) > 16 {
		return cfg, errors.New("JWKS host allowlist too large")
	}
	for _, host := range cfg.AllowedJWKSHosts {
		if _, ok := machineJWTAllowedHost(host); !ok {
			return cfg, errors.New("JWKS host allowlist invalid")
		}
	}
	return cfg, nil
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func newTokenExchangeRuntime(cfg TokenExchangeConfig, callers map[string]*callerRuntime, lookupEnv func(string) (string, bool), client *http.Client) (*tokenExchangeRuntime, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	key, ok := lookupEnv(cfg.SigningKeyEnv)
	if !ok || len(key) < 32 {
		return nil, errors.New("token exchange signing key unavailable or too short")
	}
	ttl, err := time.ParseDuration(cfg.DefaultTTL)
	if err != nil {
		return nil, errors.New("token exchange TTL invalid")
	}
	runtime := &tokenExchangeRuntime{key: []byte(key), ttl: ttl, issuers: make(map[string]*tokenExchangeIssuer), grants: make(map[string]tokenExchangeGrant), budgets: make(map[string]*missionBudget), rates: make(map[string][]time.Time), callers: callers, client: client}
	runtime.installIssuerConfig(cfg)
	return runtime, nil
}

func missionGrantIdentity(issuer, credentialID string) string {
	sum := sha256.Sum256([]byte(issuer + "\x00" + credentialID))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (rt *tokenExchangeRuntime) installIssuerConfig(cfg TokenExchangeConfig) {
	if rt == nil {
		return
	}
	issuers := make(map[string]*tokenExchangeIssuer)
	grants := make(map[string]tokenExchangeGrant)
	rt.mu.RLock()
	previousIssuers := rt.issuers
	rt.mu.RUnlock()
	if cfg.Enabled {
		for _, configured := range cfg.Issuers {
			if previous := previousIssuers[configured.IssuerURL]; previous != nil && reflect.DeepEqual(previous.config, configured) {
				issuers[configured.IssuerURL] = previous
				for _, grant := range previous.grants {
					grants[grant.id] = grant
				}
				continue
			}
			profile := adminMachineJWTConfig{Issuer: configured.IssuerURL, Audience: configured.Audience, Kind: "federated", Algorithms: append([]string(nil), configured.Algorithms...), AllowedJWKSHosts: append([]string(nil), configured.AllowedJWKSHosts...), AllowPrivateNetwork: configured.AllowPrivateNetwork, AllowLegacyTokenType: true}
			issuer := &tokenExchangeIssuer{config: cloneTokenExchangeIssuerConfig(configured), profile: profile, taskClaim: configured.TaskClaim, client: rt.client, grants: make(map[string]tokenExchangeGrant)}
			for _, subject := range configured.Subjects {
				credentialKey := subject.Claim + "\x00" + subject.Value
				profile.Subjects = append(profile.Subjects, adminMachineJWTSubject{Claim: subject.Claim, Value: subject.Value, Subject: subject.CallerID})
				grant := tokenExchangeGrant{config: subject, id: missionGrantIdentity(configured.IssuerURL, subject.Claim+":"+subject.Value), key: credentialKey}
				issuer.grants[credentialKey] = grant
				grants[grant.id] = grant
			}
			issuer.profile = profile
			issuers[configured.IssuerURL] = issuer
		}
	}
	rt.mu.Lock()
	rt.issuers = issuers
	rt.grants = grants
	rt.mu.Unlock()
	// Removing a grant also removes its process-local cap state. A still-live
	// bearer for the removed grant is rejected by verifyAccessToken.
	rt.budgetMu.Lock()
	for id := range rt.budgets {
		if _, active := grants[id]; !active {
			delete(rt.budgets, id)
		}
	}
	rt.budgetMu.Unlock()
	rt.rateMu.Lock()
	for id := range rt.rates {
		if _, active := grants[id]; !active {
			delete(rt.rates, id)
		}
	}
	rt.rateMu.Unlock()
}

func cloneTokenExchangeIssuerConfig(config TokenExchangeIssuerConfig) TokenExchangeIssuerConfig {
	config.Algorithms = append([]string(nil), config.Algorithms...)
	config.AllowedJWKSHosts = append([]string(nil), config.AllowedJWKSHosts...)
	config.Subjects = append([]TokenExchangeSubjectGrant(nil), config.Subjects...)
	for i := range config.Subjects {
		config.Subjects[i].AllowedGroups = append([]string(nil), config.Subjects[i].AllowedGroups...)
		config.Subjects[i].AllowedTools = append([]string(nil), config.Subjects[i].AllowedTools...)
	}
	return config
}

func (rt *tokenExchangeRuntime) currentIssuer(issuer string) *tokenExchangeIssuer {
	if rt == nil {
		return nil
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.issuers[issuer]
}

func (rt *tokenExchangeRuntime) currentGrant(id string) (tokenExchangeGrant, bool) {
	if rt == nil || id == "" {
		return tokenExchangeGrant{}, false
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	grant, ok := rt.grants[id]
	return grant, ok
}

func (rt *tokenExchangeRuntime) currentCaller(id string) *callerRuntime {
	if rt == nil {
		return nil
	}
	if rt.quota != nil {
		return rt.quota.lookupCaller(id)
	}
	return rt.callers[id]
}

func (rt *tokenExchangeRuntime) allowExchange(grantID string, now time.Time) bool {
	rt.rateMu.Lock()
	defer rt.rateMu.Unlock()
	if rt.rates == nil {
		rt.rates = make(map[string][]time.Time)
	}
	cutoff := now.Add(-time.Minute)
	events := rt.rates[grantID][:0]
	for _, at := range rt.rates[grantID] {
		if at.After(cutoff) {
			events = append(events, at)
		}
	}
	if len(events) >= tokenExchangeRPM {
		rt.rates[grantID] = events
		return false
	}
	rt.rates[grantID] = append(events, now)
	return true
}

func (rt *tokenExchangeRuntime) admitMission(claims *missionTokenClaims, requested int, estimatedSpend float64) bool {
	if rt == nil || claims == nil || claims.ID == "" || requested < 1 || estimatedSpend < 0 || math.IsNaN(estimatedSpend) || math.IsInf(estimatedSpend, 0) {
		return false
	}
	key := claims.GrantID
	if key == "" { // unit-only legacy fixtures; production tokens always carry a grant id.
		key = claims.ID
	}
	rt.budgetMu.Lock()
	defer rt.budgetMu.Unlock()
	if rt.budgets == nil {
		rt.budgets = make(map[string]*missionBudget)
	}
	budget := rt.budgets[key]
	if budget == nil {
		budget = &missionBudget{}
		rt.budgets[key] = budget
	}
	if budget.usedTokens+budget.reservedTokens+requested > claims.MaxTokens || budget.usedSpend+budget.reservedSpend+estimatedSpend > claims.MaxSpendUSD {
		return false
	}
	budget.reservedTokens += requested
	budget.reservedSpend += estimatedSpend
	return true
}

func (rt *tokenExchangeRuntime) settleMission(claims *missionTokenClaims, reservedTokens int, reservedSpend float64, usage Usage, spend float64) {
	if rt == nil || claims == nil {
		return
	}
	key := missionBudgetKey(claims)
	rt.budgetMu.Lock()
	defer rt.budgetMu.Unlock()
	budget := rt.budgets[key]
	if budget == nil {
		return
	}
	budget.reservedTokens = max(0, budget.reservedTokens-reservedTokens)
	budget.reservedSpend = math.Max(0, budget.reservedSpend-reservedSpend)
	budget.usedTokens += totalTokens(usage)
	budget.usedSpend += math.Max(0, spend)
}

func (rt *tokenExchangeRuntime) remainingMission(claims *missionTokenClaims) (int, float64) {
	if rt == nil || claims == nil {
		return 0, 0
	}
	key := missionBudgetKey(claims)
	rt.budgetMu.Lock()
	defer rt.budgetMu.Unlock()
	budget := rt.budgets[key]
	if budget == nil {
		return claims.MaxTokens, claims.MaxSpendUSD
	}
	return max(0, claims.MaxTokens-budget.usedTokens-budget.reservedTokens), math.Max(0, claims.MaxSpendUSD-budget.usedSpend-budget.reservedSpend)
}

func missionBudgetKey(claims *missionTokenClaims) string {
	if claims == nil {
		return ""
	}
	if claims.GrantID != "" {
		return claims.GrantID
	}
	return claims.ID
}

func (rt *tokenExchangeRuntime) exchange(ctx context.Context, raw string, groups []string, taskID string) (string, int, error) {
	token, expires, _, _, err := rt.exchangeWithDetails(ctx, raw, groups, taskID)
	return token, expires, err
}

func (rt *tokenExchangeRuntime) exchangeWithDetails(ctx context.Context, raw string, groups []string, taskID string) (string, int, string, string, error) {
	if rt == nil || raw == "" || len(raw) > maxMachineCredentialBytes || len(taskID) > maxTaskIDBytes || strings.ContainsAny(taskID, "\r\n") {
		return "", 0, "", "", ErrInvalidCredential
	}
	requestedTaskID := strings.TrimSpace(taskID)
	token, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil {
		return "", 0, "", "", ErrInvalidCredential
	}
	var hint struct {
		Issuer string `json:"iss"`
	}
	if token.UnsafeClaimsWithoutVerification(&hint) != nil || len(hint.Issuer) > 2048 {
		return "", 0, "", "", ErrInvalidCredential
	}
	issuer := rt.currentIssuer(hint.Issuer)
	if issuer == nil {
		return "", 0, "", "", ErrInvalidCredential
	}
	auth, err := issuer.getAuthenticator(ctx)
	if err != nil {
		return "", 0, "", "", err
	}
	verified, err := auth.verifyToken(ctx, raw)
	if err != nil {
		return "", 0, "", "", ErrInvalidCredential
	}
	if len(verified.confirmation) != 0 {
		return "", 0, "", "", ErrInvalidCredential
	}
	grantKey := strings.Replace(verified.principal.CredentialID, ":", "\x00", 1)
	grant, ok := issuer.grants[grantKey]
	if !ok || grant.config.CallerID != verified.principal.Subject {
		return "", 0, "", "", ErrInvalidCredential
	}
	resolvedTaskID := grant.config.TaskID
	if resolvedTaskID == "" && issuer.taskClaim != "" {
		var present bool
		resolvedTaskID, present = stringJWTClaimAtPath(verified.claims, issuer.taskClaim)
		if !present {
			return "", 0, "", "", ErrInvalidCredential
		}
	}
	if resolvedTaskID != "" && !validMissionTaskID(resolvedTaskID) {
		return "", 0, "", "", ErrInvalidCredential
	}
	// A form task id only confirms an attribution source already pinned by
	// configuration or the verified workload claim; it cannot create one.
	if requestedTaskID != "" && (resolvedTaskID == "" || requestedTaskID != resolvedTaskID) {
		return "", 0, "", "", ErrInvalidCredential
	}
	caller := rt.currentCaller(grant.config.CallerID)
	if caller == nil || caller.authPolicyError() != nil {
		return "", 0, "", "", ErrInvalidCredential
	}
	grant.id = missionGrantIdentity(hint.Issuer, verified.principal.CredentialID)
	if !rt.allowExchange(grant.id, time.Now()) {
		return "", 0, "", "", errTokenExchangeRateLimited
	}
	allowed := make(map[string]bool, len(grant.config.AllowedGroups))
	for _, group := range grant.config.AllowedGroups {
		allowed[group] = true
	}
	if len(groups) == 0 {
		return "", 0, "", "", ErrInvalidCredential
	}
	issuedGroups := make([]string, 0, len(groups))
	seen := map[string]bool{}
	for _, group := range groups {
		if !allowed[group] || seen[group] {
			return "", 0, "", "", ErrInvalidCredential
		}
		seen[group] = true
		issuedGroups = append(issuedGroups, group)
	}
	if len(issuedGroups) == 0 {
		return "", 0, "", "", ErrInvalidCredential
	}
	var rawID [18]byte
	if _, err := rand.Read(rawID[:]); err != nil {
		return "", 0, "", "", ErrInvalidCredential
	}
	jti := base64.RawURLEncoding.EncodeToString(rawID[:])
	now := time.Now().UTC()
	claims := missionTokenClaims{Claims: jwt.Claims{Issuer: missionTokenIssuer, Subject: grant.config.CallerID, Audience: jwt.Audience{missionTokenAudience}, Expiry: jwt.NewNumericDate(now.Add(rt.ttl)), IssuedAt: jwt.NewNumericDate(now), ID: jti}, CallerID: grant.config.CallerID, Groups: issuedGroups, MaxSpendUSD: grant.config.MaxSpendUSD, MaxTokens: grant.config.MaxTokens, AllowedTools: append([]string(nil), grant.config.AllowedTools...), TaskID: resolvedTaskID, HumanOwner: grant.config.HumanOwner, GrantID: grant.id}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: rt.key}, (&jose.SignerOptions{}).WithType("at+jwt"))
	if err != nil {
		return "", 0, "", "", ErrInvalidCredential
	}
	accessToken, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		return "", 0, "", "", ErrInvalidCredential
	}
	return accessToken, int(rt.ttl.Seconds()), grant.config.CallerID, grant.id, nil
}

func stringJWTClaimAtPath(claims map[string]json.RawMessage, path string) (string, bool) {
	if !validJWTClaimPath(path) {
		return "", false
	}
	parts := strings.Split(path, ".")
	current := claims
	for i, part := range parts {
		raw, ok := current[part]
		if !ok {
			return "", false
		}
		if i == len(parts)-1 {
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return "", false
			}
			return value, true
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) != nil || nested == nil {
			return "", false
		}
		current = nested
	}
	return "", false
}

var errTokenExchangeUnavailable = errors.New("token exchange issuer unavailable")
var errTokenExchangeRateLimited = errors.New("token exchange rate limited")

func validMissionTaskID(value string) bool {
	if len(value) == 0 || len(value) > maxTaskIDBytes || !isMissionTaskAlnum(value[0]) {
		return false
	}
	for _, ch := range value[1:] {
		if isMissionTaskAlnum(byte(ch)) || ch == '-' || ch == '_' || ch == '.' || ch == ':' {
			continue
		}
		return false
	}
	return true
}

func isMissionTaskAlnum(ch byte) bool {
	return ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9'
}

func (issuer *tokenExchangeIssuer) getAuthenticator(ctx context.Context) (*adminMachineJWTAuthenticator, error) {
	issuer.mu.Lock()
	defer issuer.mu.Unlock()
	if issuer.auth != nil {
		if !issuer.auth.cfg.AllowLegacyTokenType {
			issuer.auth.cfg.AllowLegacyTokenType = true
		}
		return issuer.auth, nil
	}
	if time.Now().Before(issuer.retryAt) {
		return nil, errTokenExchangeUnavailable
	}
	auth, err := newAdminMachineJWTAuthenticator(ctx, issuer.profile, issuer.client)
	if err != nil {
		if issuer.backoff == 0 {
			issuer.backoff = time.Second
		} else {
			issuer.backoff *= 2
		}
		if issuer.backoff > time.Minute {
			issuer.backoff = time.Minute
		}
		issuer.retryAt = time.Now().Add(issuer.backoff)
		slog.Warn("token exchange issuer discovery unavailable; retry scheduled", "issuer", issuer.profile.Issuer, "retry_in", issuer.backoff)
		return nil, fmt.Errorf("%w: %v", errTokenExchangeUnavailable, err)
	}
	if !auth.cfg.AllowLegacyTokenType {
		auth.cfg.AllowLegacyTokenType = true
	}
	issuer.auth = auth
	issuer.backoff = 0
	issuer.retryAt = time.Time{}
	return auth, nil
}

func (rt *tokenExchangeRuntime) verifyAccessToken(raw string) (*callerRuntime, string, *missionTokenClaims, error) {
	if rt == nil || raw == "" || len(raw) > maxMachineCredentialBytes {
		return nil, "invalid-token", nil, ErrInvalidCredential
	}
	token, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.HS256})
	if err != nil || len(token.Headers) != 1 || token.Headers[0].Algorithm != string(jose.HS256) || token.Headers[0].KeyID != "" || token.Headers[0].ExtraHeaders[jose.HeaderKey("typ")] != "at+jwt" {
		return nil, "invalid-token", nil, ErrInvalidCredential
	}
	var claims missionTokenClaims
	if token.Claims(rt.key, &claims) != nil {
		return nil, "invalid-token", nil, ErrInvalidCredential
	}
	now := time.Now()
	if claims.Issuer != missionTokenIssuer || !claims.Audience.Contains(missionTokenAudience) || claims.Subject == "" || claims.Subject != claims.CallerID || claims.ID == "" || claims.Expiry == nil || claims.IssuedAt == nil || (claims.NotBefore != nil && now.Before(claims.NotBefore.Time())) || !now.Before(claims.Expiry.Time()) || claims.IssuedAt.Time().After(now.Add(30*time.Second)) || claims.Expiry.Time().Sub(claims.IssuedAt.Time()) > rt.ttl || !finitePositive(claims.MaxSpendUSD) || claims.MaxTokens < 1 || claims.MaxTokens > 1_000_000 || len(claims.Groups) == 0 || len(claims.TaskID) > maxTaskIDBytes || len(claims.HumanOwner) > 256 || claims.HumanOwner == "" {
		return nil, "invalid-token", nil, ErrInvalidCredential
	}
	caller := rt.currentCaller(claims.CallerID)
	if caller == nil || caller.authPolicyError() != nil {
		return nil, "invalid-token", nil, ErrInvalidCredential
	}
	if claims.TaskID != "" && !validMissionTaskID(claims.TaskID) {
		return nil, "invalid-token", nil, ErrInvalidCredential
	}
	if rt.quota != nil {
		grant, ok := rt.currentGrant(claims.GrantID)
		if !ok || grant.config.CallerID != claims.CallerID || claims.MaxSpendUSD > grant.config.MaxSpendUSD || claims.MaxTokens > grant.config.MaxTokens || claims.HumanOwner != grant.config.HumanOwner {
			return nil, "invalid-token", nil, ErrInvalidCredential
		}
		allowed := make(map[string]bool, len(grant.config.AllowedGroups))
		for _, group := range grant.config.AllowedGroups {
			allowed[group] = true
		}
		for _, group := range claims.Groups {
			if !allowed[group] {
				return nil, "invalid-token", nil, ErrInvalidCredential
			}
		}
		allowedTools := make(map[string]bool, len(grant.config.AllowedTools))
		for _, tool := range grant.config.AllowedTools {
			allowedTools[tool] = true
		}
		for _, tool := range claims.AllowedTools {
			if !allowedTools[tool] {
				return nil, "invalid-token", nil, ErrInvalidCredential
			}
		}
	}
	for _, group := range claims.Groups {
		if !caller.allow[group] {
			return nil, "invalid-token", nil, ErrInvalidCredential
		}
	}
	return caller, "mission_" + claims.ID, &claims, nil
}

func projectForbidsLongLivedKeys(cfg *Config, project string) bool {
	if cfg == nil {
		return false
	}
	project = normalizeAccountID(project)
	for _, configured := range cfg.Projects {
		if normalizeAccountID(configured.ID) == project {
			return configured.ForbidLongLivedKeys
		}
	}
	return false
}

type tokenExchangeRequest struct {
	GrantType        string
	SubjectToken     string
	SubjectTokenType string
	Scope            []string
	TaskID           string
}

func parseTokenExchangeRequest(r *http.Request) (tokenExchangeRequest, error) {
	var out tokenExchangeRequest
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/x-www-form-urlencoded" || r.URL.RawQuery != "" {
		return out, ErrInvalidCredential
	}
	r.Body = http.MaxBytesReader(nil, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		return out, err
	}
	for _, key := range []string{"grant_type", "subject_token", "subject_token_type"} {
		if len(r.PostForm[key]) != 1 {
			return out, ErrInvalidCredential
		}
	}
	out.GrantType = r.Form.Get("grant_type")
	out.SubjectToken = r.Form.Get("subject_token")
	out.SubjectTokenType = r.Form.Get("subject_token_type")
	if len(r.PostForm["scope"]) != 1 {
		return out, ErrInvalidCredential
	}
	out.Scope = strings.Fields(r.PostForm.Get("scope"))
	if len(r.PostForm["task_id"]) > 1 {
		return out, ErrInvalidCredential
	}
	out.TaskID = r.Form.Get("task_id")
	if out.GrantType != tokenExchangeGrantType || out.SubjectTokenType != jwtSubjectTokenType || out.SubjectToken == "" || len(out.Scope) == 0 || len(out.Scope) > 64 || (out.TaskID != "" && !validMissionTaskID(out.TaskID)) {
		return out, ErrInvalidCredential
	}
	return out, nil
}

func (s *Service) handleTokenExchange(w http.ResponseWriter, r *http.Request) {
	writeError := func() {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
	}
	input, err := parseTokenExchangeRequest(r)
	if err != nil {
		s.recordTokenExchangeEvent(r, http.StatusBadRequest, "invalid_request", "api_auth_failed", "", "")
		writeError()
		return
	}
	token, expires, callerID, grantID, err := s.tokenExchange.exchangeWithDetails(r.Context(), input.SubjectToken, input.Scope, input.TaskID)
	if err != nil {
		reason := "invalid_grant"
		if errors.Is(err, errTokenExchangeUnavailable) {
			reason = "issuer_unavailable"
		} else if errors.Is(err, errTokenExchangeRateLimited) {
			reason = "subject_rate_limited"
		}
		status := http.StatusUnauthorized
		if errors.Is(err, errTokenExchangeUnavailable) {
			status = http.StatusServiceUnavailable
		} else if errors.Is(err, errTokenExchangeRateLimited) {
			status = http.StatusTooManyRequests
			w.Header().Set("Retry-After", "60")
		}
		s.recordTokenExchangeEvent(r, status, reason, "api_auth_failed", "", "")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if errors.Is(err, errTokenExchangeUnavailable) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"temporarily_unavailable"}`))
		} else if errors.Is(err, errTokenExchangeRateLimited) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"slow_down"}`))
		} else {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		}
		return
	}
	s.recordTokenExchangeEvent(r, http.StatusOK, "mission_token_issued", "authentication", callerID, grantID)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "issued_token_type": "urn:ietf:params:oauth:token-type:access_token", "token_type": "Bearer", "expires_in": expires, "scope": strings.Join(input.Scope, " ")})
}

func (s *Service) recordTokenExchangeEvent(r *http.Request, status int, reason, eventType, callerID, grantID string) {
	if s == nil || (!s.securityReportsEnabled() && s.securityEventStream == nil) {
		return
	}
	ip := clientIPInfo{}
	if r != nil && s.cfg != nil {
		ip = resolveClientIP(r, s.cfg.Server.ClientIP)
	}
	s.recordSecurityAccessEvent(securityAccessEvent{TS: time.Now().UTC(), RequestID: requestID(), EventType: eventType, Surface: "token_exchange", HTTPMethod: http.MethodPost, PathTemplate: "/oauth/token", StatusCode: status, Outcome: securityOutcome(status), ReasonCode: reason, CallerID: callerID, OperationID: grantID, Client: inferClient(r), UserAgentFamily: userAgentFamily(r.UserAgent()), IPAddress: ip.Address, IPVersion: ip.Version, IPSource: ip.Source, TrustedProxyApplied: ip.TrustedProxyApplied, RequestIsPrivate: ip.IsPrivate, RequestIsLoopback: ip.IsLoopback, RequestIsReserved: ip.IsReserved})
}

func missionSpendCeiling(rc *requestContext) SpendCeilingConfig {
	if rc == nil || rc.mission == nil {
		return SpendCeilingConfig{}
	}
	value := rc.mission.MaxSpendUSD
	if rc.s != nil && rc.s.tokenExchange != nil {
		_, value = rc.s.tokenExchange.remainingMission(rc.mission)
		// pick() runs after this request atomically reserves its estimated
		// spend. Restore only this request's reservation for target affordability
		// checks; reservations held by concurrent requests remain excluded.
		value += rc.missionReservedSpend
	}
	return SpendCeilingConfig{MaxEstimatedCostUSD: &value}
}

func missionTokenRouteAllowed(method, path string) bool {
	switch method {
	case http.MethodPost:
		switch path {
		case "/v1/chat/completions", "/v1/responses", "/v1/messages", "/anthropic/v1/messages",
			"/v1/messages/count_tokens", "/anthropic/v1/messages/count_tokens",
			"/v1/responses/input_tokens", "/v1/embeddings":
			return true
		}
	case http.MethodGet:
		return path == "/v1/models" || strings.HasPrefix(path, "/v1/models/") && strings.TrimPrefix(path, "/v1/models/") != ""
	}
	return false
}

// withMissionTokenRouteAllowList rejects mission bearers before dispatch on any
// caller-authenticated or otherwise public serving route outside the inference
// allow-list. This keeps authorization centralized and prevents a token from
// inheriting new route capabilities when handlers are added later.
func (s *Service) withMissionTokenRouteAllowList(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s != nil && s.tokenExchange != nil && !missionTokenRouteAllowed(r.Method, r.URL.Path) {
			token := callerCredentialFromRequest(r)
			if strings.HasPrefix(token, "eyJ") {
				_, _, mission, err := s.tokenExchange.verifyAccessToken(token)
				if err != nil || mission == nil {
					next.ServeHTTP(w, r)
					return
				}
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthorized", "message": "authentication required"}})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func callerCredentialFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	const prefix = "Bearer "
	if strings.HasPrefix(r.Header.Get("Authorization"), prefix) {
		return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), prefix))
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

func (rc *requestContext) callerCanUseGroup(group string) bool {
	if rc == nil || rc.caller == nil || !rc.caller.allow[group] {
		return false
	}
	if rc.mission == nil {
		return true
	}
	for _, allowed := range rc.mission.Groups {
		if allowed == group {
			return true
		}
	}
	return false
}

func (rc *requestContext) filterMissionModelMetadata(models []map[string]any) []map[string]any {
	if rc == nil || rc.mission == nil {
		return models
	}
	allowed := make(map[string]bool, len(rc.mission.Groups))
	for _, group := range rc.mission.Groups {
		allowed[group] = true
	}
	out := make([]map[string]any, 0, len(models))
	for _, model := range models {
		name, _ := model["id"].(string)
		if allowed[name] {
			out = append(out, model)
		}
	}
	return out
}

func missionToolsAllowed(rc *requestContext, req *IRRequest) bool {
	if rc == nil || rc.mission == nil {
		return true
	}
	allowed := make(map[string]bool, len(rc.mission.AllowedTools))
	for _, name := range rc.mission.AllowedTools {
		allowed[name] = true
	}
	for _, tool := range req.Tools {
		name := ""
		if fn, ok := tool["function"].(map[string]any); ok {
			name, _ = fn["name"].(string)
		}
		if name == "" {
			name, _ = tool["name"].(string)
		}
		if !allowed[name] {
			return false
		}
	}
	return true
}

func missionTokenLimit(rc *requestContext, req *IRRequest) bool {
	if rc == nil || rc.mission == nil {
		return true
	}
	remaining := rc.mission.MaxTokens
	if rc.s != nil && rc.s.tokenExchange != nil {
		remaining, _ = rc.s.tokenExchange.remainingMission(rc.mission)
	}
	inputEstimate := estimateTokens(req)
	if inputEstimate >= remaining {
		return false
	}
	remaining -= inputEstimate
	if req.MaxTokens > 0 && req.MaxTokens < remaining {
		remaining = req.MaxTokens
	}
	req.MaxTokens = remaining
	field := req.MaxTokensField
	if field == "" {
		if rc.dialect == "openai-responses" {
			field = "max_output_tokens"
		} else {
			field = "max_tokens"
		}
		req.MaxTokensField = field
	}
	if req.Raw == nil {
		req.Raw = make(map[string]any)
	}
	req.Raw[field] = req.MaxTokens
	return true
}

func (s *Service) admitMissionRequest(rc *requestContext, requestedTokens int, estimatedSpend float64) bool {
	if rc == nil || rc.mission == nil {
		return true
	}
	if s.tokenExchange == nil || !s.tokenExchange.admitMission(rc.mission, requestedTokens, estimatedSpend) {
		return false
	}
	rc.missionAdmitted = true
	rc.missionReservedTokens = requestedTokens
	rc.missionReservedSpend = estimatedSpend
	return true
}

func missionRequestSpendEstimate(group ModelGroup, inputTokens, outputTokens int) float64 {
	var inputRate, outputRate float64
	for _, target := range group.Targets {
		inputRate = math.Max(inputRate, target.InputPricePerMillionUSD)
		outputRate = math.Max(outputRate, target.OutputPricePerMillionUSD)
	}
	return (float64(inputTokens)*inputRate + float64(outputTokens)*outputRate) / 1_000_000
}
