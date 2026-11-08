// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Tests for admin_machine_credential_events.go against the durable #869 sink.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sqliteDriver "github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type machineEventRecordingSink struct {
	mu     sync.Mutex
	events []AdminCredentialEvent
	fail   error
}

func (r *machineEventRecordingSink) RecordCredentialEvent(_ context.Context, event AdminCredentialEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.events = append(r.events, event)
	return nil
}

func TestRuntimeMachineCredentialEventsDurable(t *testing.T) {
	recorder := &machineEventRecordingSink{}
	sink, err := newAdminMachineCredentialEventSink(recorder)
	if err != nil {
		t.Fatal(err)
	}
	// Successful use is fail-closed and redacted.
	if err := sink.record(context.Background(), adminMachineCredentialEvent{
		Kind: machineEventEmergency, Lifecycle: machineLifecycleUse, CredentialID: "emg-1",
		Subject: "emergency:ops", Issuer: "offline", RequestID: "req-1", ClientAddr: "127.0.0.1",
		Success: true, Reason: machineReasonNone,
	}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.events) != 1 || recorder.events[0].Outcome != "success" || recorder.events[0].Lifecycle != "use" {
		t.Fatalf("use event not recorded durably: %+v", recorder.events)
	}
	if recorder.events[0].ClientIPHash == "" || strings.Contains(recorder.events[0].ClientIPHash, "127.0.0.1") {
		t.Fatal("client address stored raw instead of hashed")
	}
	// Failed authentication is best effort.
	recorder.fail = errors.New("write failed")
	if err := sink.record(context.Background(), adminMachineCredentialEvent{Kind: machineEventOAuth, Lifecycle: machineLifecycleAuthenticate, Success: false, Reason: machineReasonInvalid}); err != nil {
		t.Fatal("failed authentication must not fail closed on audit error")
	}
	// Successful authentication/lifecycle and every emergency operation fail closed.
	for _, event := range []adminMachineCredentialEvent{
		{Kind: machineEventMTLS, Lifecycle: machineLifecycleAuthenticate, Success: true},
		{Kind: machineEventEmergency, Lifecycle: machineLifecycleActivate, Success: true},
		{Kind: machineEventEmergency, Lifecycle: machineLifecycleDeactivate, Success: true},
	} {
		if err := sink.record(context.Background(), event); err == nil {
			t.Fatalf("successful %s/%s event ignored audit failure", event.Kind, event.Lifecycle)
		}
	}
	recorder.fail = nil
	// Bounded enumerations and reason/outcome consistency.
	for _, bad := range []adminMachineCredentialEvent{
		{Kind: "browser", Lifecycle: machineLifecycleAuthenticate, Success: true},
		{Kind: machineEventOAuth, Lifecycle: "login", Success: true},
		{Kind: machineEventOAuth, Lifecycle: machineLifecycleAuthenticate, Success: false},
		{Kind: machineEventOAuth, Lifecycle: machineLifecycleAuthenticate, Success: true, Reason: machineReasonInvalid},
		{Kind: machineEventOAuth, Lifecycle: machineLifecycleAuthenticate, Success: false, Reason: "raw parser text"},
	} {
		if err := sink.record(context.Background(), bad); err == nil {
			t.Fatalf("invalid enumeration/consistency accepted: %+v", bad)
		}
	}
	// Error mapping stays enumerated; no error text leaks.
	if machineReasonForError(ErrSenderConstraintFailed) != machineReasonSenderConstraint ||
		machineReasonForError(ErrNoCredential) != machineReasonRequired ||
		machineReasonForError(ErrUnsupportedAuthentication) != machineReasonUnsupported ||
		machineReasonForError(errors.New("anything")) != machineReasonInvalid {
		t.Fatal("typed error mapping not bounded")
	}
	// A nil sink fails closed rather than silently dropping events.
	if _, err := newAdminMachineCredentialEventSink(nil); err == nil {
		t.Fatal("nil sink accepted")
	}
}

// failingCredentialEventSink is an unavailable audit store: successful
// machine authentication must fail closed against it.
type failingCredentialEventSink struct{}

func (f *failingCredentialEventSink) RecordCredentialEvent(context.Context, AdminCredentialEvent) error {
	return errors.New("event store unavailable")
}

func mustMachineEventSink(t *testing.T) *adminMachineCredentialEventSink {
	t.Helper()
	sink, err := newAdminMachineCredentialEventSink(&failingCredentialEventSink{})
	if err != nil {
		t.Fatal(err)
	}
	return sink
}

func mustActivationStore(t *testing.T) *adminEmergencyActivationStore {
	t.Helper()
	db, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), "activation.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyAdminEmergencyActivationSchema(db); err != nil {
		t.Fatal(err)
	}
	store, err := newAdminEmergencyActivationStore(db, 1, time.Now, func(reason string) string { return reason })
	if err != nil {
		t.Fatal(err)
	}
	return store
}
