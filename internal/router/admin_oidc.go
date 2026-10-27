// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	adminOIDCLoginTTL                 = 10 * time.Minute
	adminOIDCMaxPendingLogin          = 256
	adminOIDCMaxPendingLoginPerClient = 32
)

type adminAuthSubject struct {
	subject     string
	domain      string
	source      string
	email       string
	issuer      string
	groups      []string
	permissions map[string]bool
}

type adminOIDCRuntime struct {
	cfg        AdminOIDCConfig
	oauth2     *oauth2.Config
	verifier   *oidc.IDTokenVerifier
	stateStore *adminOIDCStateStore
}

type adminOIDCLoginState struct {
	state        string
	nonce        string
	codeVerifier string
	clientKey    string
	expiresAt    time.Time
}

type adminOIDCStateStore struct {
	mu     sync.Mutex
	states map[string]adminOIDCLoginState
}

type adminSessionStore struct {
	mu       sync.Mutex
	cfg      AdminSessionConfig
	sessions map[string]adminSession
	now      func() time.Time
}

type adminSession struct {
	cookieValue string
	subject     adminAuthSubject
	expiresAt   time.Time
}

func newAdminOIDCRuntime(ctx context.Context, cfg AdminOIDCConfig, httpClient *http.Client) (*adminOIDCRuntime, error) {
	clientID := strings.TrimSpace(os.Getenv(strings.TrimSpace(cfg.ClientIDEnv)))
	clientSecret := strings.TrimSpace(os.Getenv(strings.TrimSpace(cfg.ClientSecretEnv)))
	oidcCtx := ctx
	if httpClient != nil {
		oidcCtx = oidc.ClientContext(ctx, httpClient)
	}
	provider, err := oidc.NewProvider(oidcCtx, strings.TrimRight(strings.TrimSpace(cfg.IssuerURL), "/"))
	if err != nil {
		return nil, fmt.Errorf("initialize admin OIDC provider: %w", err)
	}
	scopes := normalizeOIDCScopes(cfg.Scopes)
	oauthConfig := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  strings.TrimSpace(cfg.RedirectURL),
		Scopes:       scopes,
	}
	return &adminOIDCRuntime{
		cfg:      cfg,
		oauth2:   oauthConfig,
		verifier: provider.Verifier(&oidc.Config{ClientID: clientID}),
		stateStore: &adminOIDCStateStore{
			states: map[string]adminOIDCLoginState{},
		},
	}, nil
}

func normalizeOIDCScopes(configured []string) []string {
	seen := map[string]bool{"openid": true}
	scopes := []string{"openid"}
	defaults := []string{"email", "profile"}
	if len(configured) == 0 {
		configured = defaults
	}
	for _, scope := range configured {
		scope = strings.TrimSpace(scope)
		if scope == "" || seen[scope] {
			continue
		}
		seen[scope] = true
		scopes = append(scopes, scope)
	}
	return scopes
}

func (s *Service) handleAdminOIDCLogin(w http.ResponseWriter, r *http.Request) {
	s.setAdminAuthHeaders(w)
	if s == nil || s.adminOIDC == nil {
		http.NotFound(w, r)
		return
	}
	login, err := newAdminOIDCLoginState(time.Now().UTC())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"type": "oidc-login-failed", "message": "oidc-login-failed"}})
		return
	}
	login.clientKey = adminOIDCClientKey(r, s.cfg.Server.ClientIP)
	if !s.adminOIDC.stateStore.put(login, time.Now().UTC()) {
		s.recordAdminSecurityAccess(r, adminAuthSubject{}, http.StatusTooManyRequests, "oidc-login-rate-limited", "admin:auth", "login")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]any{"type": "oidc-login-rate-limited", "message": "oidc-login-rate-limited"}})
		return
	}
	challenge := pkceChallenge(login.codeVerifier)
	redirectURL := s.adminOIDC.oauth2.AuthCodeURL(
		login.state,
		oidc.Nonce(login.nonce),
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

func (s *Service) handleAdminOIDCCallback(w http.ResponseWriter, r *http.Request) {
	s.setAdminAuthHeaders(w)
	if s == nil || s.adminOIDC == nil {
		http.NotFound(w, r)
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("error")) != "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"type": "oidc-callback-failed", "message": "oidc-callback-failed"}})
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if state == "" || code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "oidc-callback-invalid", "message": "oidc-callback-invalid"}})
		return
	}
	login, ok := s.adminOIDC.stateStore.take(state, time.Now().UTC())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"type": "oidc-state-invalid", "message": "oidc-state-invalid"}})
		return
	}
	ctx := r.Context()
	if s.httpClient != nil {
		ctx = oidc.ClientContext(ctx, s.httpClient)
	}
	token, err := s.adminOIDC.oauth2.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", login.codeVerifier))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"type": "oidc-token-invalid", "message": "oidc-token-invalid"}})
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || strings.TrimSpace(rawIDToken) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"type": "oidc-token-invalid", "message": "oidc-token-invalid"}})
		return
	}
	idToken, err := s.adminOIDC.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"type": "oidc-token-invalid", "message": "oidc-token-invalid"}})
		return
	}
	if idToken.Nonce != login.nonce {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"type": "oidc-nonce-invalid", "message": "oidc-nonce-invalid"}})
		return
	}
	subject, err := s.adminOIDC.subjectFromIDToken(idToken)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"type": "oidc-subject-invalid", "message": "oidc-subject-invalid"}})
		return
	}
	session, err := s.adminSession.create(subject)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"type": "session-create-failed", "message": "session-create-failed"}})
		return
	}
	http.SetCookie(w, s.adminSession.cookie(session.cookieValue, session.expiresAt))
	http.Redirect(w, r, "/admin/auth/me", http.StatusFound)
}

func (s *Service) handleAdminOIDCLogout(w http.ResponseWriter, r *http.Request) {
	s.setAdminAuthHeaders(w)
	if s == nil || s.adminOIDC == nil {
		http.NotFound(w, r)
		return
	}
	if cookie, err := r.Cookie(s.adminSession.cookieName()); err == nil {
		s.adminSession.delete(cookie.Value)
	}
	http.SetCookie(w, s.adminSession.expiredCookie())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Service) handleAdminAuthMe(w http.ResponseWriter, r *http.Request) {
	subject, ok := s.authenticateAdminSubject(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, safeAdminSubjectResponse(subject))
}

func (s *Service) authenticateAdminSubject(w http.ResponseWriter, r *http.Request) (adminAuthSubject, bool) {
	s.setAdminAuthHeaders(w)
	if s == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"type": "unauthorized", "message": "unauthorized"}})
		return adminAuthSubject{}, false
	}
	if !s.hasAdminAuthMechanism() {
		http.NotFound(w, r)
		return adminAuthSubject{}, false
	}
	// Dedicated admin API keys (#350) are a distinct bearer credential
	// namespace (rtr_admin_*). They are checked first; a bearer token that
	// looks like an admin key but fails verification is rejected outright
	// rather than falling through to another mechanism. Caller and provider
	// tokens never authenticate here.
	if bearer := bearerTokenFromRequest(r); strings.HasPrefix(bearer, adminAPIKeyPrefix+"_") {
		if subject, ok := s.authenticateAdminAPIKey(bearer); ok {
			return subject, true
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"type": "unauthorized", "message": "unauthorized"}})
		return adminAuthSubject{}, false
	}
	if s.adminOIDC != nil {
		if subject, ok := s.adminSession.subjectFromRequest(r); ok {
			return subject, true
		}
	}
	if s.cfg.Server.AdminAuth.Basic.Enabled {
		if _, _, hasBasic := r.BasicAuth(); hasBasic || s.adminOIDC == nil {
			basic, ok := s.authenticateAdminBasic(w, r)
			if !ok {
				return adminAuthSubject{}, false
			}
			return adminAuthSubjectForBasic(basic), true
		}
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"type": "unauthorized", "message": "unauthorized"}})
	return adminAuthSubject{}, false
}

func adminAuthSubjectForBasic(subject adminBasicRuntime) adminAuthSubject {
	return adminAuthSubject{
		subject:     subject.subject,
		domain:      subject.domain,
		source:      "basic",
		permissions: subject.permissions,
	}
}

func safeAdminSubjectResponse(subject adminAuthSubject) map[string]any {
	body := map[string]any{
		"ok":      true,
		"source":  subject.source,
		"subject": subject.subject,
		"domain":  subject.domain,
	}
	if subject.email != "" {
		body["email"] = subject.email
	}
	if subject.issuer != "" {
		body["issuer"] = subject.issuer
	}
	if len(subject.groups) > 0 {
		body["groups"] = append([]string(nil), subject.groups...)
	}
	return body
}

func newAdminOIDCLoginState(now time.Time) (adminOIDCLoginState, error) {
	state, err := randomURLToken(32)
	if err != nil {
		return adminOIDCLoginState{}, err
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		return adminOIDCLoginState{}, err
	}
	codeVerifier, err := randomURLToken(32)
	if err != nil {
		return adminOIDCLoginState{}, err
	}
	return adminOIDCLoginState{state: state, nonce: nonce, codeVerifier: codeVerifier, expiresAt: now.Add(adminOIDCLoginTTL)}, nil
}

func (s *adminOIDCStateStore) put(state adminOIDCLoginState, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	clientPending := 0
	for key, existing := range s.states {
		if !existing.expiresAt.After(now) {
			delete(s.states, key)
			continue
		}
		if existing.clientKey == state.clientKey {
			clientPending++
		}
	}
	if clientPending >= adminOIDCMaxPendingLoginPerClient {
		return false
	}
	if len(s.states) >= adminOIDCMaxPendingLogin {
		return false
	}
	s.states[stateHash(state.state)] = state
	return true
}

func adminOIDCClientKey(r *http.Request, cfg ClientIPConfig) string {
	if r == nil {
		return "unknown"
	}
	storeIP := true
	cfg.StoreIP = &storeIP
	ip := resolveClientIP(r, cfg).Address
	if ip == "" {
		ip = strings.TrimSpace(r.RemoteAddr)
	}
	if ip == "" {
		return "unknown"
	}
	return ip
}

func (s *adminOIDCStateStore) take(state string, now time.Time) (adminOIDCLoginState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := stateHash(state)
	login, ok := s.states[key]
	if ok {
		delete(s.states, key)
	}
	if !ok || !login.expiresAt.After(now) {
		return adminOIDCLoginState{}, false
	}
	if subtle.ConstantTimeCompare([]byte(login.state), []byte(state)) != 1 {
		return adminOIDCLoginState{}, false
	}
	return login, true
}

func (r *adminOIDCRuntime) subjectFromIDToken(idToken *oidc.IDToken) (adminAuthSubject, error) {
	if idToken == nil {
		return adminAuthSubject{}, fmt.Errorf("missing id token")
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return adminAuthSubject{}, err
	}
	email := strings.ToLower(strings.TrimSpace(claimString(claims, r.cfg.EmailClaim)))
	if email != "" {
		if !strings.Contains(email, "@") {
			return adminAuthSubject{}, fmt.Errorf("email claim is not an email address")
		}
		if verified, present := claimBool(claims, "email_verified"); present && !verified {
			return adminAuthSubject{}, fmt.Errorf("email is not verified")
		}
	}
	if len(r.cfg.AllowedDomains) > 0 {
		if email == "" {
			return adminAuthSubject{}, fmt.Errorf("email is required for allowed_domains")
		}
		if !emailDomainAllowed(email, r.cfg.AllowedDomains) {
			return adminAuthSubject{}, fmt.Errorf("email domain is not allowed")
		}
	}
	subjectValue := strings.TrimSpace(claimString(claims, r.cfg.SubjectClaim))
	if subjectValue == "" {
		subjectValue = strings.TrimSpace(idToken.Subject)
	}
	if subjectValue == "" {
		return adminAuthSubject{}, fmt.Errorf("missing subject")
	}
	subject := oidcSubjectID(r.cfg.SubjectClaim, subjectValue)
	groups := claimStringList(claims, r.cfg.GroupsClaim)
	return adminAuthSubject{
		subject: subject,
		domain:  strings.TrimSpace(r.cfg.Domain),
		source:  "oidc_session",
		email:   email,
		issuer:  idToken.Issuer,
		groups:  groups,
	}, nil
}

func oidcSubjectID(claimName, value string) string {
	value = strings.TrimSpace(value)
	if strings.EqualFold(strings.TrimSpace(claimName), "email") || strings.Contains(value, "@") {
		return "user:" + strings.ToLower(value)
	}
	return "oidc:" + sanitizeSubjectComponent(value)
}

func sanitizeSubjectComponent(value string) string {
	value = strings.TrimSpace(value)
	replacer := strings.NewReplacer(" ", "_", "\t", "_", "\r", "_", "\n", "_", ",", "_")
	return replacer.Replace(value)
}

func emailDomainAllowed(email string, allowed []string) bool {
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return false
	}
	domain := strings.ToLower(strings.TrimSpace(parts[1]))
	for _, allowedDomain := range allowed {
		if domain == strings.ToLower(strings.TrimSpace(allowedDomain)) {
			return true
		}
	}
	return false
}

func claimString(claims map[string]any, name string) string {
	if claims == nil {
		return ""
	}
	switch v := claims[strings.TrimSpace(name)].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func claimBool(claims map[string]any, name string) (bool, bool) {
	if claims == nil {
		return false, false
	}
	switch v := claims[strings.TrimSpace(name)].(type) {
	case bool:
		return v, true
	case string:
		if strings.EqualFold(v, "true") {
			return true, true
		}
		if strings.EqualFold(v, "false") {
			return false, true
		}
	}
	return false, false
}

func claimStringList(claims map[string]any, name string) []string {
	if claims == nil {
		return nil
	}
	var out []string
	switch v := claims[strings.TrimSpace(name)].(type) {
	case []string:
		out = append(out, v...)
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	case string:
		for _, item := range strings.Split(v, ",") {
			out = append(out, item)
		}
	}
	cleaned := out[:0]
	seen := map[string]bool{}
	for _, item := range out {
		item = strings.TrimSpace(item)
		if item == "" || strings.ContainsAny(item, "\r\n") || seen[item] {
			continue
		}
		seen[item] = true
		cleaned = append(cleaned, item)
	}
	return cleaned
}

func newAdminSessionStore(cfg AdminSessionConfig) *adminSessionStore {
	return &adminSessionStore{
		cfg:      cfg,
		sessions: map[string]adminSession{},
		now:      func() time.Time { return time.Now().UTC() },
	}
}

func (s *adminSessionStore) create(subject adminAuthSubject) (adminSession, error) {
	if s == nil {
		return adminSession{}, fmt.Errorf("session store is nil")
	}
	cookieValue, err := randomURLToken(32)
	if err != nil {
		return adminSession{}, err
	}
	now := s.now()
	session := adminSession{
		cookieValue: cookieValue,
		subject:     subject,
		expiresAt:   now.Add(s.ttl()),
	}
	key := sessionHash(cookieValue)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[key] = session
	for existingKey, existing := range s.sessions {
		if !existing.expiresAt.After(now) {
			delete(s.sessions, existingKey)
		}
	}
	return session, nil
}

func (s *adminSessionStore) subjectFromRequest(r *http.Request) (adminAuthSubject, bool) {
	if s == nil || r == nil {
		return adminAuthSubject{}, false
	}
	cookie, err := r.Cookie(s.cookieName())
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return adminAuthSubject{}, false
	}
	key := sessionHash(cookie.Value)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[key]
	if !ok || !session.expiresAt.After(now) {
		delete(s.sessions, key)
		return adminAuthSubject{}, false
	}
	if subtle.ConstantTimeCompare([]byte(session.cookieValue), []byte(cookie.Value)) != 1 {
		return adminAuthSubject{}, false
	}
	return session.subject, true
}

func (s *adminSessionStore) delete(cookieValue string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionHash(cookieValue))
}

func (s *adminSessionStore) cookie(value string, expires time.Time) *http.Cookie {
	// Secure is set dynamically (false positive): secureCookies() defaults to true and
	// only permits cleartext cookies for explicitly configured non-TLS local admin
	// deployments (metrum-ai/router#412).
	return &http.Cookie{ // nosemgrep: go.lang.security.audit.net.cookie-missing-secure.cookie-missing-secure
		Name:     s.cookieName(),
		Value:    value,
		Path:     "/admin",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: s.sameSite(),
	}
}

func (s *adminSessionStore) expiredCookie() *http.Cookie {
	// Secure is set dynamically (false positive): secureCookies() defaults to true and
	// only permits cleartext cookies for explicitly configured non-TLS local admin
	// deployments (metrum-ai/router#412). This cookie clears the session, no value.
	return &http.Cookie{ // nosemgrep: go.lang.security.audit.net.cookie-missing-secure.cookie-missing-secure
		Name:     s.cookieName(),
		Value:    "",
		Path:     "/admin",
		Expires:  time.Unix(0, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: s.sameSite(),
	}
}

func (s *adminSessionStore) cookieName() string {
	if s == nil || strings.TrimSpace(s.cfg.CookieName) == "" {
		return "smart_router_admin_session"
	}
	return strings.TrimSpace(s.cfg.CookieName)
}

func (s *adminSessionStore) ttl() time.Duration {
	if s == nil || s.cfg.TTL == 0 {
		return 8 * time.Hour
	}
	return s.cfg.TTL
}

func (s *adminSessionStore) secureCookies() bool {
	return s == nil || s.cfg.SecureCookies == nil || *s.cfg.SecureCookies
}

func (s *adminSessionStore) sameSite() http.SameSite {
	if s == nil {
		return http.SameSiteStrictMode
	}
	switch strings.ToLower(strings.TrimSpace(s.cfg.SameSite)) {
	case "lax":
		return http.SameSiteLaxMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteStrictMode
	}
}

func randomURLToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func stateHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func sessionHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
