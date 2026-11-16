// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

const containmentWebhookPath = "/admin/v1/containment/webhook"

type containmentWebhookAuthenticator struct {
	owner *Service
	next  Authenticator
}

func (s *Service) withContainmentWebhook(next Authenticator) Authenticator {
	if s == nil || next == nil {
		return next
	}
	return containmentWebhookAuthenticator{owner: s, next: next}
}

func (a containmentWebhookAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if r.URL.Path != containmentWebhookPath {
		return a.next.Authenticate(ctx, r)
	}
	if a.owner == nil || a.owner.cfg == nil || !a.owner.cfg.Server.ContainmentWebhook.Enabled {
		return Principal{}, ErrInvalidCredential
	}
	cfg := a.owner.cfg.Server.ContainmentWebhook
	secret := os.Getenv(strings.TrimSpace(cfg.SecretEnv))
	if len(secret) < 32 {
		return Principal{}, ErrInvalidCredential
	}
	window := 5 * time.Minute
	if strings.TrimSpace(cfg.TimestampWindow) != "" {
		parsed, err := time.ParseDuration(strings.TrimSpace(cfg.TimestampWindow))
		if err != nil || parsed <= 0 || parsed > 10*time.Minute {
			return Principal{}, ErrInvalidCredential
		}
		window = parsed
	}
	timestamp := strings.TrimSpace(r.Header.Get("X-Containment-Timestamp"))
	nonce := strings.TrimSpace(r.Header.Get("X-Containment-Nonce"))
	signature := strings.TrimPrefix(strings.TrimSpace(r.Header.Get("X-Containment-Signature")), "sha256=")
	if timestamp == "" || !validContainmentNonce(nonce) {
		return Principal{}, ErrInvalidCredential
	}
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return Principal{}, ErrInvalidCredential
	}
	now := a.owner.adminNow()
	when := time.Unix(unix, 0)
	delta := now.Sub(when)
	if delta < -window || delta > window {
		return Principal{}, ErrInvalidCredential
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, (16<<10)+1))
	if err != nil || len(body) > 16<<10 {
		return Principal{}, ErrInvalidCredential
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "\n" + nonce + "\n"))
	_, _ = mac.Write(body)
	want := mac.Sum(nil)
	got, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(got, want) {
		return Principal{}, ErrInvalidCredential
	}
	digest := sha256.Sum256(body)
	return Principal{Subject: authzRoleContainment, Kind: "webhook", CredentialID: "containment-webhook",
		containmentNonce: nonce, containmentBodyHash: hex.EncodeToString(digest[:]), containmentRetention: 2 * window}, nil
}

func validContainmentNonce(nonce string) bool {
	if len(nonce) < 16 || len(nonce) > 128 {
		return false
	}
	for _, r := range nonce {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			return false
		}
	}
	return true
}

func (s *Service) claimContainmentNonce(tx *gorm.DB, nonce, requestHash string, retention time.Duration, now time.Time) error {
	if s == nil {
		return ErrAuthorizationUnavailable
	}
	store, ok := s.adminIdempotencyStore()
	if !ok {
		return ErrAuthorizationUnavailable
	}
	sqlStore, ok := store.(*sqlAdminIdempotencyStore)
	if !ok {
		return ErrAuthorizationUnavailable
	}
	scope := adminIdempotencyScope{
		PrincipalKind: "webhook", PrincipalSubject: authzRoleContainment, Domain: AdminGlobalDomain,
		Operation: "containment.webhook", Key: nonce, RequestHash: requestHash,
	}
	if tx == nil {
		return ErrAuthorizationUnavailable
	}
	return sqlStore.beginWithTTLDB(tx, scope, now, retention)
}
