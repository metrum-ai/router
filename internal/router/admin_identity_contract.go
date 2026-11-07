// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// The machine-identity, denial and credential-event contracts the /admin/v1 mount
// (metrum-ai/router#811) depends on. The durable audit persistence that consumes them
// is metrum-ai/router#816.

import (
	"context"
	"strings"
	"time"
)

// AdminAuditMeta is the audit context of one admin request. The contract layer
// resolves it from the authenticated principal and the request, and the write path
// stores it with the change.
type AdminAuditMeta struct {
	Subject      string
	Kind         string
	CredentialID string
	Issuer       string
	Domain       string
	Object       string
	Action       string
	OperationID  string
	RequestID    string
	Ticket       string
	RequestHash  string
}

type adminAuditMetaContextKey struct{}

// WithAdminAuditMeta records the audit context for a request.
func WithAdminAuditMeta(ctx context.Context, meta AdminAuditMeta) context.Context {
	return context.WithValue(ctx, adminAuditMetaContextKey{}, meta)
}

// AdminAuditMetaFrom returns the audit context recorded for a request.
func AdminAuditMetaFrom(ctx context.Context) (AdminAuditMeta, bool) {
	meta, ok := ctx.Value(adminAuditMetaContextKey{}).(AdminAuditMeta)
	return meta, ok
}

// AdminDenialRecord is one durable denial record: an authorization refusal or a
// rate-limit refusal.
type AdminDenialRecord struct {
	At               time.Time
	PrincipalKind    string
	PrincipalSubject string
	CredentialID     string
	Issuer           string
	Domain           string
	Object           string
	Action           string
	OperationID      string
	RequestID        string
	ReasonCode       string
	Path             string
	Status           int
}

// AdminDenialSink records denials durably. The mount fails closed (5xx) when a
// denial cannot be recorded, so a refusal is never silent.
type AdminDenialSink interface {
	RecordDenial(ctx context.Context, record AdminDenialRecord) error
}

// AdminCredentialEvent is one authentication or credential-lifecycle record
// (ACP-066).
type AdminCredentialEvent struct {
	EventID      string
	At           time.Time
	Kind         string
	CredentialID string
	Subject      string
	Issuer       string
	Outcome      string
	ReasonCode   string
	RequestID    string
	Lifecycle    string
}

// AdminCredentialEventSink records authentication and lifecycle events. A
// successful authentication must be recorded (fail closed); a failed one is
// best effort so an attacker cannot make the plane unavailable by forcing audit
// writes to fail.
type AdminCredentialEventSink interface {
	RecordCredentialEvent(ctx context.Context, event AdminCredentialEvent) error
}

// AdminFreshnessSource supplies the policy and configuration revision gauges
// (#813). The zero value and ok=false omit the series rather than publish a
// misleading zero.
type AdminFreshnessSource interface {
	PolicyFreshness() (desired, applied int64, observedAt time.Time, ok bool)
	ConfigFreshness() (desired, applied int64, observedAt time.Time, ok bool)
}

const (
	maxAdminTicketBytes = 200
	maxAdminAuditField  = 300
)

// safeAdminTicket bounds and redacts an operator-supplied ticket reference.
func safeAdminTicket(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) > maxAdminTicketBytes {
		raw = raw[:maxAdminTicketBytes]
	}
	return truncateSafeDetail(redactDiagnosticSecrets(raw))
}

// adminAuditFieldValue bounds and redacts one audit field.
func adminAuditFieldValue(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) <= maxAdminAuditField {
		return truncateSafeDetail(redactDiagnosticSecrets(raw))
	}
	// Redact first, then truncate on a rune boundary so a multi-byte character cannot
	// be split into an invalid sequence.
	redacted := truncateSafeDetail(redactDiagnosticSecrets(raw))
	runes := []rune(redacted)
	if len(runes) > maxAdminAuditField {
		redacted = string(runes[:maxAdminAuditField])
	}
	return redacted
}
