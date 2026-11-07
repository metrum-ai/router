// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Admin-plane audit records (ACP-022/023, metrum-ai/router#816).
//
// Every mutating /admin/v1 operation writes exactly one audit row in the SAME
// transaction as its change, in the database where the change lives:
//
//	control-plane changes (config sets, groups, shadow, callers)
//	  → router_config_change_events, extended with the audit columns by
//	    config control-plane schema phase 11
//	usage-database changes (content-capture delete/purge, training labels)
//	  → router_admin_audit_events (usage scope, additive)
//
// A failed audit insert fails the change: the insert shares the transaction and
// its error aborts it, so there is no path that commits a change without its
// record.
//
// Records carry identifiers only. No header, token, verifier, claim, request body
// or upstream text is ever stored, and free text (reason, ticket, detail) is run
// through the same redactor the diagnostics surface uses.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
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

// adminAuditMetaFor builds the audit context of an operation from its principal
// and request. The ticket is the operator-supplied incident reference, bounded
// and redacted.
func adminAuditMetaFor(principal Principal, domain string, op Operation, r *requestAuditSource) AdminAuditMeta {
	meta := AdminAuditMeta{
		Subject:      principal.Subject,
		Kind:         principal.Kind,
		CredentialID: principal.CredentialID,
		Issuer:       principal.Issuer,
		Domain:       domain,
		Object:       op.Object,
		Action:       op.Action,
		OperationID:  op.ID,
	}
	if r != nil {
		meta.RequestID = r.RequestID
		meta.Ticket = safeAdminTicket(r.Ticket)
	}
	return meta
}

// requestAuditSource is the request-level part of the audit context.
type requestAuditSource struct {
	RequestID string
	Ticket    string
}

const (
	maxAdminTicketBytes = 200
	maxAdminAuditField  = 300
)

// safeAdminTicket bounds and redacts an operator-supplied ticket or reason.
func safeAdminTicket(raw string) string {
	return truncateSafeDetail(redactDiagnosticSecrets(strings.TrimSpace(raw[0:min(len(raw), maxAdminTicketBytes)])))
}

// adminAuditHash returns a short stable digest of a representation, used for the
// before/after hashes of a change. It is a digest only: the content is never
// stored in the audit row.
func adminAuditHash(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// AdminAuditRecord is one durable admin audit row. The same shape is stored in the
// control-plane change-event row and in the usage-side audit table.
type AdminAuditRecord struct {
	EventID          string
	At               time.Time
	RuntimeScope     string
	PrincipalSubject string
	PrincipalKind    string
	CredentialID     string
	Issuer           string
	Domain           string
	Object           string
	Action           string
	OperationID      string
	RequestID        string
	Outcome          string
	ReasonCode       string
	Ticket           string
	ConfigSetID      string
	PrevConfigSetID  string
	ConfigRevision   int64
	BeforeHash       string
	AfterHash        string
	IdempotencyKey   string
	RequestHash      string
	Detail           string
}

// AdminAuditSink is the durable sink the write paths call. #816 supplies it; the
// admin mount's Audit hook is a separate, non-durable observation.
type AdminAuditSink interface {
	// RecordChange writes one audit row inside the caller's transaction. A
	// non-nil error must abort the change.
	RecordChange(ctx context.Context, record AdminAuditRecord) error
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

// adminAuditFieldValue bounds and redacts one audit field.
func adminAuditFieldValue(raw string) string {
	if len(raw) > maxAdminAuditField {
		raw = raw[:maxAdminAuditField]
	}
	return truncateSafeDetail(redactDiagnosticSecrets(strings.TrimSpace(raw)))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// adminAuditMetaForWrite returns the audit context for one write. On /admin/v1
// the contract layer already recorded the full context (principal kind, credential
// ID, issuer, domain, operation, request ID, ticket); on the legacy surface the
// same fields are derived from the authenticated admin subject so a v4.7 write is
// audited too. Either way the row is written in the change's transaction.
func adminAuditMetaForWrite(r *http.Request, subject adminAuthSubject, object, action string) AdminAuditMeta {
	if r != nil {
		if meta, ok := AdminAuditMetaFrom(r.Context()); ok {
			return meta
		}
	}
	meta := AdminAuditMeta{
		Subject: subject.subject,
		Kind:    defaultString(strings.TrimSpace(subject.source), "unknown"),
		Domain:  defaultString(strings.TrimSpace(subject.domain), adminGlobalDomain),
		Object:  object,
		Action:  action,
	}
	if subject.principal != nil {
		meta.Kind = subject.principal.Kind
		meta.CredentialID = subject.principal.CredentialID
		meta.Issuer = subject.principal.Issuer
	}
	if r != nil {
		meta.RequestID = r.Header.Get(adminRequestIDHeader)
	}
	return meta
}
