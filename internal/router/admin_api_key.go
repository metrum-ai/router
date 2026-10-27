// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Dedicated admin API keys for /admin/* routes (metrum-ai/router#350).
//
// Admin keys are a third credential class, distinct from caller API keys
// (rtr_metrum_*, model API access) and provider/upstream credentials
// (api_key_env references, never accepted on inbound requests). An admin key
// authenticates only /admin/* routes and grants exactly the fine-grained
// permissions configured on it; it can never authenticate as a caller, and a
// caller or provider key can never authenticate as an admin key because the
// token namespaces are disjoint by construction.
//
// Token shape: "rtr_admin_<key_id>_<secret>". The key_id segment is public
// metadata used to select the configured verifier; the secret is 256 bits of
// randomness. At rest the router stores only a PBKDF2-HMAC-SHA3-512 verifier
// (the same self-describing token_hash format as caller tokens), resolved
// from the environment variable named by key_hash_env. Raw keys are never
// logged, stored, or returned outside the one-time generation output.

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

const (
	// adminAPIKeyPrefix namespaces admin keys away from caller tokens
	// (rtr_metrum_*) so the two credential classes can never collide.
	adminAPIKeyPrefix      = "rtr_admin"
	adminAPIKeySecretBytes = 32
)

// Fine-grained admin permissions introduced with the admin API key. The same
// permission names apply to admin OIDC users through the Casbin object/action
// mapping in adminPermissionObjectAction.
const (
	adminPermissionConfigRead     = "config:read"
	adminPermissionConfigWrite    = "config:write"
	adminPermissionConfigActivate = "config:activate"
	adminPermissionConfigForce    = "config:force"
	adminPermissionShadowWrite    = "shadow:write"
	adminPermissionAuthRead       = "admin:auth:read"
)

const (
	authzObjectAdminConfig = "admin:config"
	authzObjectAdminShadow = "admin:shadow"
)

// knownAdminKeyPermissions is the allowlist of permissions an admin key (or a
// browser-admin user) may hold. Config validation rejects anything else so a
// typo fails closed at startup instead of silently granting nothing.
var knownAdminKeyPermissions = map[string]bool{
	adminPermissionConfigRead:       true,
	adminPermissionConfigWrite:      true,
	adminPermissionConfigActivate:   true,
	adminPermissionConfigForce:      true,
	adminPermissionShadowWrite:      true,
	adminPermissionAuthRead:         true,
	"admin:callers:list":            true,
	"admin:callers:issue":           true,
	"admin:callers:rotate":          true,
	"admin:callers:revoke":          true,
	"admin:reports:read":            true,
	"admin:reports:export":          true,
	"admin:reports:drilldown":       true,
	"admin:security_reports:read":   true,
	"admin:security_reports:export": true,
	"admin:training:export":         true,
	"admin:training:label":          true,
	"metrics:read":                  true,
}

// adminPermissionObjectAction maps a fine-grained permission to the Casbin
// object/action pair enforced for OIDC and policy-granted subjects. The new
// config and shadow permissions live under admin-namespaced objects; existing
// "object:action" style permissions keep their object verbatim.
func adminPermissionObjectAction(permission string) (object, action string, ok bool) {
	permission = strings.TrimSpace(permission)
	switch permission {
	case adminPermissionConfigRead:
		return authzObjectAdminConfig, authzActionRead, true
	case adminPermissionConfigWrite:
		return authzObjectAdminConfig, "write", true
	case adminPermissionConfigActivate:
		return authzObjectAdminConfig, "activate", true
	case adminPermissionConfigForce:
		return authzObjectAdminConfig, "force", true
	case adminPermissionShadowWrite:
		return authzObjectAdminShadow, "write", true
	}
	// Generic rule: the last colon segment is the action, the head is the
	// object (admin:reports:read -> admin:reports / read).
	idx := strings.LastIndex(permission, ":")
	if idx <= 0 || idx == len(permission)-1 {
		return "", "", false
	}
	return permission[:idx], permission[idx+1:], true
}

// GeneratedAdminAPIKey is the one-time output of admin key generation. The
// raw token is shown to the operator once and never stored; only TokenHash
// is configured on the router.
type GeneratedAdminAPIKey struct {
	KeyID     string `json:"key_id"`
	Token     string `json:"token"`
	TokenHash string `json:"token_hash"`
}

// validAdminKeyID reports whether keyID is usable inside the
// "rtr_admin_<key_id>_<secret>" token shape: lowercase ASCII letters, digits,
// and "-", 1-64 characters. Underscores are excluded so the key_id/secret
// split is unambiguous.
func validAdminKeyID(keyID string) bool {
	if keyID == "" || len(keyID) > 64 {
		return false
	}
	for _, r := range keyID {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

// GenerateAdminAPIKey creates a new admin key and its stored verifier. The
// caller shows the token to the operator exactly once and configures only
// TokenHash (via the environment variable named by key_hash_env).
func GenerateAdminAPIKey(keyID string, reader io.Reader, iterations int) (GeneratedAdminAPIKey, error) {
	keyID = strings.TrimSpace(keyID)
	if !validAdminKeyID(keyID) {
		return GeneratedAdminAPIKey{}, fmt.Errorf("admin key_id %q is invalid: use 1-64 of [a-z0-9-]", keyID)
	}
	if reader == nil {
		reader = rand.Reader
	}
	secretRaw := make([]byte, adminAPIKeySecretBytes)
	if _, err := io.ReadFull(reader, secretRaw); err != nil {
		return GeneratedAdminAPIKey{}, fmt.Errorf("generate admin key secret: %w", err)
	}
	token := adminAPIKeyPrefix + "_" + keyID + "_" + base64.RawURLEncoding.EncodeToString(secretRaw)
	tokenHash, err := HashCallerToken(token, reader, iterations)
	if err != nil {
		return GeneratedAdminAPIKey{}, err
	}
	return GeneratedAdminAPIKey{KeyID: keyID, Token: token, TokenHash: tokenHash}, nil
}

// parseAdminAPIKeyToken splits an "rtr_admin_<key_id>_<secret>" bearer token
// into its public key id. ok is false for any other token shape; the raw
// token is never echoed.
func parseAdminAPIKeyToken(token string) (keyID string, ok bool) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, adminAPIKeyPrefix+"_") {
		return "", false
	}
	rest := token[len(adminAPIKeyPrefix)+1:]
	idx := strings.IndexByte(rest, '_')
	if idx <= 0 || idx == len(rest)-1 {
		return "", false
	}
	keyID = rest[:idx]
	if !validAdminKeyID(keyID) {
		return "", false
	}
	if len(rest)-idx-1 < 16 {
		return "", false
	}
	return keyID, true
}

// adminAPIKeyRuntime is the verified-key runtime for one configured admin
// key. The verified slot caches the keyed digest of the last token that
// passed the PBKDF2 check so steady-state admin requests cost one HMAC,
// mirroring the caller-token design in caller_auth.go.
type adminAPIKeyRuntime struct {
	keyID       string
	subject     string
	domain      string
	permissions map[string]bool
	verifier    callerTokenVerifier
	verified    atomic.Pointer[callerAuthDigest]
}

// buildAdminAPIKeyRuntimes resolves configured admin keys into runtimes.
// Config validation has already checked shape and verifier format; entries
// that fail here are skipped defensively and can never authenticate.
func buildAdminAPIKeyRuntimes(cfg *Config, lookupEnv func(string) (string, bool)) map[string]*adminAPIKeyRuntime {
	out := map[string]*adminAPIKeyRuntime{}
	if cfg == nil {
		return out
	}
	for _, key := range cfg.Server.AdminAuth.APIKeys {
		entry := buildAdminAPIKeyRuntime(key, lookupEnv)
		if entry != nil {
			out[entry.keyID] = entry
		}
	}
	return out
}

func buildAdminAPIKeyRuntime(key AdminAPIKeyConfig, lookupEnv func(string) (string, bool)) *adminAPIKeyRuntime {
	if strings.TrimSpace(key.Status) == "disabled" {
		return nil
	}
	keyID := strings.TrimSpace(key.KeyID)
	hash, ok := lookupEnv(strings.TrimSpace(key.KeyHashEnv))
	if !ok {
		return nil
	}
	verifier, err := parseCallerTokenHash(strings.TrimSpace(hash))
	if err != nil {
		return nil
	}
	subject := strings.TrimSpace(key.Subject)
	if subject == "" {
		subject = "adminkey:" + keyID
	}
	permissions := map[string]bool{}
	for _, permission := range key.Permissions {
		permissions[strings.TrimSpace(permission)] = true
	}
	return &adminAPIKeyRuntime{
		keyID:       keyID,
		subject:     subject,
		domain:      strings.TrimSpace(key.Domain),
		permissions: permissions,
		verifier:    verifier,
	}
}

// authenticateAdminAPIKey verifies an "rtr_admin_*" bearer token against the
// configured admin keys. It never authenticates caller or provider tokens:
// those lack the admin prefix and fail closed here. The raw token is never
// logged; the returned subject carries only the safe adminkey:<key_id>
// identity and the configured permission set.
func (s *Service) authenticateAdminAPIKey(token string) (adminAuthSubject, bool) {
	if s == nil {
		return adminAuthSubject{}, false
	}
	keyID, ok := parseAdminAPIKeyToken(token)
	if !ok {
		return adminAuthSubject{}, false
	}
	runtime := s.adminAPIKeys[keyID]
	if runtime == nil {
		return adminAuthSubject{}, false
	}
	if s.adminKeyDigestMatches(runtime, token) {
		return adminAuthSubjectForAPIKey(runtime), true
	}
	// Bound KDF work under a flood of wrong secrets for a known key id,
	// sharing the caller-auth semaphore.
	st := s.callerAuth
	if st != nil && st.kdfSlots != nil {
		st.kdfSlots <- struct{}{}
		defer func() { <-st.kdfSlots }()
	}
	if !runtime.verifier.matches(token) {
		return adminAuthSubject{}, false
	}
	if st != nil {
		digest := st.digest(token)
		runtime.verified.Store(&digest)
	}
	return adminAuthSubjectForAPIKey(runtime), true
}

func adminAuthSubjectForAPIKey(runtime *adminAPIKeyRuntime) adminAuthSubject {
	return adminAuthSubject{
		subject:     runtime.subject,
		domain:      runtime.domain,
		source:      "admin_key",
		permissions: runtime.permissions,
	}
}

// adminKeyDigestMatches reports whether token equals the cached verified
// digest for this key. The digest is HMAC-SHA3-512 under a per-process random
// key, so a config reload or restart simply re-runs the KDF once.
func (s *Service) adminKeyDigestMatches(runtime *adminAPIKeyRuntime, token string) bool {
	st := s.callerAuth
	if st == nil {
		return false
	}
	cached := runtime.verified.Load()
	if cached == nil {
		return false
	}
	digest := st.digest(token)
	return hmac.Equal(cached[:], digest[:])
}

// authorizeAdminPermission enforces one fine-grained admin permission. A
// direct grant on the authenticated subject (basic users and admin keys
// carry explicit permission sets) is honored first; otherwise the mapped
// Casbin object/action is enforced so OIDC users receive the same
// permissions through policy. Admin key permissions are deliberately NOT
// projected into Casbin policy lines: exact permission-map checks keep the
// key surface independent of matcher semantics.
func (s *Service) authorizeAdminPermission(subject adminAuthSubject, permission string) bool {
	if subject.permissions[permission] {
		return true
	}
	object, action, ok := adminPermissionObjectAction(permission)
	if !ok {
		return false
	}
	return s.authorizeAdmin(subject, object, action)
}

// authorizeAdminObjectAction enforces an existing Casbin object/action pair
// (for example admin:callers/list), additionally honoring a matching direct
// permission grant ("object:action") on basic users and admin API keys so
// the dedicated admin key can operate the pre-existing admin endpoints
// without requiring parallel Casbin policy.
func (s *Service) authorizeAdminObjectAction(subject adminAuthSubject, object, action string) bool {
	if subject.permissions[object+":"+action] {
		return true
	}
	return s.authorizeAdmin(subject, object, action)
}

// hasAdminAuthMechanism reports whether any admin authentication mechanism is
// configured. When none is, /admin/* auth routes stay 404 and authenticated
// admin endpoints return 401, matching the pre-#350 surface.
func (s *Service) hasAdminAuthMechanism() bool {
	if s == nil || s.cfg == nil {
		return false
	}
	if s.adminOIDC != nil || s.cfg.Server.AdminAuth.Basic.Enabled {
		return true
	}
	return len(s.adminAPIKeys) > 0
}

// bearerTokenFromRequest extracts the Authorization bearer token, or "" when
// the header is absent or not a bearer credential.
func bearerTokenFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) < len("Bearer ") || !strings.EqualFold(header[:len("Bearer ")], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(header[len("Bearer "):])
}
