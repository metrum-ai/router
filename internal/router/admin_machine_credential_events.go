// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// PENDING ADAPTER (#869 interface). Not compiled until #869
// (AdminCredentialEvent{Sink}, adminRedactThenTrim, insertAdminCredentialEventTx)
// merges; #857 stays draft until then. Delete the .pending suffix on rebase and
// run the paired test.
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
