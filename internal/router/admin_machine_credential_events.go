// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Adapter from machine authentication outcomes onto #869's durable
// AdminCredentialEvent sink (AdminCredentialEvent{Sink}, adminRedactThenTrim,
// insertAdminCredentialEventTx).
//
// Only bounded enumerations reach the sink: no raw credential, token, claim,
// certificate, proof, or verifier material, and no attacker-controlled text.
// A successful machine authentication/lifecycle event is fail-closed; a failed
// authentication is best effort so an attacker cannot deny service by forcing
// audit writes to fail. Use/deactivation ordering requirements are enforced by
// the caller before this adapter runs.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// adminMachineEventKind is the closed set of machine credential kinds.
type adminMachineEventKind string

const (
	machineEventMTLS      adminMachineEventKind = "mtls"
	machineEventOAuth     adminMachineEventKind = "oauth"
	machineEventFederated adminMachineEventKind = "federated"
	machineEventAdminKey  adminMachineEventKind = "adminkey"
	machineEventEmergency adminMachineEventKind = "emergency"
)

// adminMachineEventLifecycle is the closed set of lifecycle actions.
type adminMachineEventLifecycle string

const (
	machineLifecycleAuthenticate adminMachineEventLifecycle = "authenticate"
	machineLifecycleActivate     adminMachineEventLifecycle = "activate"
	machineLifecycleUse          adminMachineEventLifecycle = "use"
	machineLifecycleDeactivate   adminMachineEventLifecycle = "deactivate"
)

// adminMachineReasonCode is a bounded enumerated failure reason. Never carry
// credential text or parser output.
type adminMachineReasonCode string

const (
	machineReasonNone              adminMachineReasonCode = ""
	machineReasonRequired          adminMachineReasonCode = "authentication_required"
	machineReasonInvalid           adminMachineReasonCode = "invalid_credential"
	machineReasonUnsupported       adminMachineReasonCode = "unsupported_authentication"
	machineReasonSenderConstraint  adminMachineReasonCode = "sender_constraint_failed"
	machineReasonEmergencyInactive adminMachineReasonCode = "emergency_inactive"
	machineReasonAuditUnavailable  adminMachineReasonCode = "audit_unavailable"
)

type adminMachineCredentialEvent struct {
	Kind         adminMachineEventKind
	Lifecycle    adminMachineEventLifecycle
	CredentialID string
	Subject      string
	Issuer       string
	RequestID    string
	ClientAddr   string // hashed by the sink; never stored raw
	Success      bool
	Reason       adminMachineReasonCode
}

// adminMachineCredentialEventSink maps owned events onto the #869 durable sink.
type adminMachineCredentialEventSink struct{ sink AdminCredentialEventSink }

func newAdminMachineCredentialEventSink(sink AdminCredentialEventSink) (*adminMachineCredentialEventSink, error) {
	if sink == nil {
		return nil, errors.New("admin credential event sink required")
	}
	return &adminMachineCredentialEventSink{sink: sink}, nil
}

// record writes one event. A successful authentication/lifecycle event returns
// the sink error so the caller fails closed before identity or dispatch; a
// failed authentication is best effort.
func (s *adminMachineCredentialEventSink) record(ctx context.Context, event adminMachineCredentialEvent) error {
	if s == nil || s.sink == nil {
		return errors.New("admin credential event sink unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	record, err := s.build(event)
	if err != nil {
		return err
	}
	recordErr := s.sink.RecordCredentialEvent(ctx, record)
	if recordErr != nil && event.Success {
		return recordErr
	}
	return nil
}

func (s *adminMachineCredentialEventSink) build(event adminMachineCredentialEvent) (AdminCredentialEvent, error) {
	if !boundedMachineKind(event.Kind) || !boundedMachineLifecycle(event.Lifecycle) || !boundedMachineReason(event.Reason) {
		return AdminCredentialEvent{}, errors.New("admin credential event enumeration invalid")
	}
	if event.Success && event.Reason != machineReasonNone {
		return AdminCredentialEvent{}, errors.New("successful admin credential event carries a failure reason")
	}
	if !event.Success && event.Reason == machineReasonNone {
		return AdminCredentialEvent{}, errors.New("failed admin credential event requires a reason")
	}
	outcome := "failure"
	if event.Success {
		outcome = "success"
	}
	return AdminCredentialEvent{
		At:           time.Now().UTC(),
		Kind:         string(event.Kind),
		Lifecycle:    string(event.Lifecycle),
		CredentialID: adminRedactThenTrim(event.CredentialID, 256),
		Subject:      adminRedactThenTrim(event.Subject, 1024),
		Issuer:       adminRedactThenTrim(event.Issuer, 2048),
		RequestID:    adminRedactThenTrim(event.RequestID, 128),
		Outcome:      outcome,
		ReasonCode:   string(event.Reason),
		ClientIPHash: adminClientIPHash(event.ClientAddr),
	}, nil
}

func boundedMachineKind(kind adminMachineEventKind) bool {
	switch kind {
	case machineEventMTLS, machineEventOAuth, machineEventFederated, machineEventAdminKey, machineEventEmergency:
		return true
	}
	return false
}

func boundedMachineLifecycle(lifecycle adminMachineEventLifecycle) bool {
	switch lifecycle {
	case machineLifecycleAuthenticate, machineLifecycleActivate, machineLifecycleUse, machineLifecycleDeactivate:
		return true
	}
	return false
}

func boundedMachineReason(reason adminMachineReasonCode) bool {
	switch reason {
	case machineReasonNone, machineReasonRequired, machineReasonInvalid, machineReasonUnsupported,
		machineReasonSenderConstraint, machineReasonEmergencyInactive, machineReasonAuditUnavailable:
		return true
	}
	return false
}

// machineReasonForError maps an owned typed error to a bounded reason code.
func machineReasonForError(err error) adminMachineReasonCode {
	switch {
	case err == nil:
		return machineReasonNone
	case errors.Is(err, ErrNoCredential):
		return machineReasonRequired
	case errors.Is(err, ErrSenderConstraintFailed):
		return machineReasonSenderConstraint
	case errors.Is(err, ErrUnsupportedAuthentication):
		return machineReasonUnsupported
	case errors.Is(err, ErrInvalidCredential):
		return machineReasonInvalid
	}
	return machineReasonInvalid
}

// redactMachineEventField is the single owned redaction entry point; #869's
// redactor is authoritative once merged.
func redactMachineEventField(raw string) string {
	return adminRedactThenTrim(strings.TrimSpace(raw), 1024)
}

// adminMachineEventRecordingAuthenticator acknowledges every successful
// non-emergency machine authentication durably (fail closed) and leaves failed
// attempts best effort. Emergency identities are recorded by their own
// transaction-bound Use event, so they are not double-written here.
type adminMachineEventRecordingAuthenticator struct {
	inner  Authenticator
	events *adminMachineCredentialEventSink
}

func (a *adminMachineEventRecordingAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if a == nil || a.inner == nil {
		return Principal{}, ErrInvalidCredential
	}
	principal, err := a.inner.Authenticate(ctx, r)
	if a.events == nil || (principal.Kind != "" && principal.Kind == "emergency") {
		return principal, err
	}
	event := adminMachineCredentialEvent{
		Kind:         adminMachineEventKind(principal.Kind),
		Lifecycle:    machineLifecycleAuthenticate,
		CredentialID: principal.CredentialID,
		Subject:      principal.Subject,
		Issuer:       principal.Issuer,
		RequestID:    "",
		Success:      err == nil,
		Reason:       machineReasonForError(err),
	}
	if err == nil && !boundedMachineKind(event.Kind) {
		// A kind outside the closed set cannot be acknowledged durably: refuse
		// the authentication rather than emitting an unauditable identity.
		return Principal{}, ErrInvalidCredential
	}
	if recordErr := a.events.record(context.WithoutCancel(ctx), event); recordErr != nil && err == nil {
		return Principal{}, ErrInvalidCredential
	}
	return principal, err
}
